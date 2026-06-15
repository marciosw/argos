# Spec — GitHubPoller

> Componente de polling de Issues e gestão de labels para os 3 repositórios (`web`, `mobile`, `hybrid`).

**Status:** rascunho para validação.

---

## 1. Responsabilidades

1. Listar issues por label em cada repositório ativo (não pausado).
2. Ler e mutar labels conforme a máquina de estados (`agent:ready → documentation → todo → doing → done`).
3. Comentar nas issues (resultado, erros, link do PR).
4. Criar branch e abrir Pull Request ao concluir a codificação.
5. Respeitar rate limits e ser idempotente (não reprocessar a mesma issue/comentário).

> O GitHubPoller **não decide** transições de negócio — ele executa as operações de GitHub que o Scheduler determina. A decisão de transição é do Scheduler/lifecycle.

## 2. Autenticação e configuração

- **Fine-grained PAT** com escopo mínimo por repositório:
  - `Issues: Read and write` (ler issues, comentar, alterar labels)
  - `Contents: Read and write` (criar branch / push da branch do agente)
  - `Pull requests: Read and write` (abrir PR)
- Token via env (`GITHUB_TOKEN`) — nunca em arquivo versionado.
- Mapa de repositórios em config:

```yaml
repos:
  web:    { owner: "<org>", name: "<repo-web>" }
  mobile: { owner: "<org>", name: "<repo-mobile>" }
  hybrid: { owner: "<org>", name: "<repo-hybrid>" }
```

## 3. Cliente HTTP

- REST API v3 (`https://api.github.com`). Cliente Go próprio sobre `net/http` (sem dependência pesada) ou `go-github` — **decisão a validar** (recomendação: `net/http` + tipos mínimos, para manter o binário enxuto e sem CGO).
- Header `Authorization: Bearer <PAT>`, `Accept: application/vnd.github+json`, `X-GitHub-Api-Version: 2022-11-28`.
- Timeout por request; retry com backoff exponencial em 5xx/429.

## 4. Labels — convenção

| Label           | Cor (sugestão) | Uso |
| --------------- | -------------- | --- |
| `agent:ready`   | verde          | gatilho de entrada |
| `documentation` | azul           | specs em elaboração / aguardando aprovação |
| `todo`          | cinza          | aprovado, aguardando início de codificação |
| `doing`         | amarelo        | codificação em andamento |
| `done`          | roxo           | PR aberto |
| `agent:error`   | vermelho       | falha; requer atenção humana |

Regra de exclusividade: as labels de fase são **mutuamente exclusivas**. Toda transição remove a label anterior e adiciona a nova numa operação reconciliada (ler labels atuais → calcular diff → aplicar).

## 5. Operações (API interna do componente)

```go
type Poller interface {
    // Lista issues que têm uma das labels-alvo, no repo dado.
    ListByLabels(ctx, repo string, labels []string) ([]Issue, error)

    // Aplica a transição de label (remove fromLabel, adiciona toLabel) de forma idempotente.
    TransitionLabel(ctx, repo string, issue int, fromLabel, toLabel string) error

    // Adiciona/remove uma label avulsa (ex.: agent:error).
    SetLabel(ctx, repo string, issue int, label string) error
    ClearLabel(ctx, repo string, issue int, label string) error

    // Comenta na issue.
    Comment(ctx, repo string, issue int, body string) error

    // Cria branch a partir da base e abre PR.
    OpenPR(ctx, repo string, in OpenPRInput) (PullRequest, error)
}
```

```go
type Issue struct {
    Repo      string
    Number    int
    Title     string
    Body      string
    Labels    []string
    UpdatedAt time.Time
    URL       string
}

type OpenPRInput struct {
    Issue       int
    Branch      string   // ex.: agent/issue-42
    Base        string   // ex.: main
    Title       string
    Body        string   // inclui "Closes #N" e referência às specs
}
```

## 6. Estratégia de polling

- A cada **tick do Scheduler** (intervalo configurável, sugestão 60s), para cada repo **ativo**:
  - `ListByLabels(repo, ["agent:ready", "todo"])` — issues que o orquestrador pode pegar automaticamente.
  - Issues em `documentation`/`doing` **não** são puxadas por polling (estão sob controle de uma sessão ou aguardando humano); seu estado vive no SQLite.
- O resultado é entregue ao Scheduler, que filtra por locks/idempotência antes de despachar.

### Idempotência

- Antes de processar, o Scheduler consulta o SQLite: a issue já tem sessão ativa/lock? já foi processada nesta fase? Se sim, ignora.
- Cursor por repo: armazenar `last_polled_at`/ETag para reduzir tráfego (usar header `If-None-Match` quando aplicável).

## 7. Rate limiting

- Ler headers `X-RateLimit-Remaining` / `X-RateLimit-Reset`.
- Se `Remaining` baixo, aumentar o intervalo efetivo de polling (backoff) e logar.
- Em 429/abuse, respeitar `Retry-After`.

## 8. Abertura de PR (fase `done`)

Pré-condições: branch `agent/issue-{N}` criada e com commits (push feito pelo Claude Code dentro da sessão, **nunca** na `main`).

Fluxo:
1. Confirmar que a branch existe no remoto.
2. `OpenPR` com `base = main` (ou branch protegida configurada), corpo referenciando a issue (`Closes #N`) e os arquivos de spec.
3. Comentar na issue com o link do PR.
4. Transição de label `doing → done`.
5. Sinalizar ao TelegramGateway para notificar com o link do PR.

> **Branch protection**: o orquestrador assume que a `main` é protegida no GitHub. Ele apenas abre o PR; merge é manual pelo humano.

## 9. Tratamento de erros

- Falha de rede/5xx: retry com backoff; após N tentativas, marca `agent:error` + notifica Telegram.
- Conflito de label (estado divergente do esperado): reconciliar lendo o estado real antes de aplicar; logar divergência.
- Issue removida/fechada externamente: cancelar processamento e limpar lock/sessão no SQLite.

## 10. Pontos abertos para validação

1. Biblioteca: `net/http` próprio vs. `go-github`?
2. Nome da branch do agente (`agent/issue-{N}`?) e branch base (`main`?).
3. Usar GitHub Issues do **repo-alvo** para o fluxo, e os specs também no repo-alvo — confirmar.
4. Webhooks de GitHub (push/PR) entram no escopo agora ou só polling? (assumido: **só polling** nesta fase).
