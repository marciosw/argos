# Design — Orchestrator

> Spec de arquitetura geral do **orchestrator**: um orquestrador em Go que gerencia agentes de codificação autônomos (Claude Code CLI) a partir de GitHub Issues, com controle por Telegram.

**Status:** rascunho para validação humana — nenhum código escrito ainda.

---

## 1. Objetivo

Coordenar, de forma autônoma e com pontos de validação humana, o ciclo de vida de tarefas de desenvolvimento descritas como GitHub Issues, em 3 repositórios:

| Repo     | Stack                  |
| -------- | ---------------------- |
| `web`    | Python + React         |
| `mobile` | Flutter                |
| `hybrid` | Flutter (web + mobile) |

O orquestrador faz polling das Issues, despacha cada tarefa para uma instância do Claude Code CLI, monitora o uso da janela de contexto, persiste estado em SQLite e usa um bot do Telegram como canal de comando e validação (revisão pelo celular, sem precisar de computador).

## 2. Princípios de design

1. **Spec-driven development**: nenhuma codificação começa sem specs aprovadas pelo humano. Cada issue tem seu diretório `docs/specs/issue-{N}/`.
2. **Validação humana em dois portões**: (a) fim da documentação → aprovação via Telegram; (b) PR aberto → review/merge manual no GitHub.
3. **Idempotência**: o estado em SQLite garante que o mesmo evento (issue, comentário, sessão) nunca seja reprocessado.
4. **Retomada sem perda de contexto**: ao cruzar 65% de uso de janela, a sessão é encerrada e reiniciada com `progress.md` + `context.md` injetados.
5. **Branch protection**: os agentes só criam branches e abrem PRs; nunca fazem push direto na `main`.
6. **Falha segura**: qualquer erro de uma tarefa não derruba o orquestrador; a issue volta a um estado recuperável e o humano é notificado.

## 3. Ciclo de vida de uma issue (máquina de estados por labels)

```
agent:ready → documentation → todo → doing → done
```

| Label           | Significado / ação do agente                                                                                       | Transição |
| --------------- | ------------------------------------------------------------------------------------------------------------------ | --------- |
| `agent:ready`   | Issue pronta. Agente cria `docs/specs/issue-{N}/` e gera os arquivos de spec.                                       | → `documentation` |
| `documentation` | Agente elaborando specs. Ao concluir: envia digest + arquivos `.md` no Telegram e **aguarda aprovação humana**.    | (aguarda `/approve` ou `/reject`) |
| `todo`          | Aprovação recebida (`/approve #N`). Agente pega no próximo ciclo de polling.                                        | → `doing` |
| `doing`         | Codificação em andamento.                                                                                           | → `done` |
| `done`          | Codificação concluída. Agente abre PR, comenta na issue e notifica o Telegram com o link.                          | (fim do fluxo automático) |

Transições adicionais:
- `/reject #N motivo` em `documentation`/aguardando aprovação → volta para `documentation` com o motivo gravado em `context.md`.
- Erro durante `doing` → label de erro (`agent:error`) + notificação; estado preservado para retomada.

> O conjunto de labels é a **fonte de verdade do estado de negócio na issue**; o SQLite é a fonte de verdade do **estado operacional do orquestrador** (sessões, cursores de polling, locks, hashes processados). Os dois são reconciliados a cada ciclo.

## 4. Visão de componentes

```
                         ┌───────────────────────────────────────────────┐
                         │                 orchestrator (Go)              │
                         │                                                │
   GitHub REST API ◀────▶│  ┌─────────────┐   ┌──────────────────────┐   │
   (3 repos, PAT)        │  │ GitHubPoller │──▶│      Scheduler       │   │
                         │  └─────────────┘   │  (loop de orquestração)│  │
                         │         ▲           └──────────┬───────────┘   │
                         │         │                      │               │
   Telegram Bot API ◀───▶│  ┌──────┴───────┐      ┌───────▼────────┐      │
   (webhook)             │  │TelegramGateway│◀────▶│   TaskRunner   │─────┼──▶ claude (CLI subprocess)
                         │  └──────────────┘      └───────┬────────┘      │     stream-json
                         │                                │               │
                         │                        ┌───────▼────────┐      │
                         │                        │   Persistence  │      │
                         │                        │   (SQLite)     │      │
                         │                        └────────────────┘      │
                         │                                                │
                         │   ConfigManager (modelo por repo/issue/env)    │
                         └───────────────────────────────────────────────┘
```

### Componentes (specs detalhados em arquivos próprios)

| Componente        | Responsabilidade                                                                                  | Spec |
| ----------------- | -------------------------------------------------------------------------------------------------- | ---- |
| **Scheduler**     | Loop central. A cada tick: lê repos não pausados, reconcilia labels↔SQLite, enfileira tarefas elegíveis, despacha ao TaskRunner respeitando concorrência. | (neste arquivo) |
| **GitHubPoller**  | Lista issues por label em cada repo, lê/escreve labels, comenta, abre PR.                          | `github_poller.md` |
| **TelegramGateway** | Webhook de comandos, parsing, envio de mensagens/arquivos, fila de aprovações pendentes.         | `telegram_gateway.md` |
| **TaskRunner**    | Invoca o Claude Code CLI, parseia `stream-json`, monitora contexto, gere retomada via `progress.md`. | `task_runner.md` |
| **Persistence**   | Schema SQLite, queries, controle de estado e locks.                                                | `persistence.md` |
| **ConfigManager** | Resolve modelo e parâmetros por env / repo / issue.                                                | `model_config.md` |

## 5. Estrutura de pacotes Go (proposta)

```
orchestrator/
├── cmd/
│   └── orchestrator/
│       └── main.go              # bootstrap: config, DB, HTTP server, scheduler
├── internal/
│   ├── config/                  # ConfigManager (env + arquivo YAML/TOML)
│   │   ├── config.go
│   │   └── model.go             # resolução de modelo por repo/issue
│   ├── scheduler/               # loop de orquestração + máquina de estados
│   │   ├── scheduler.go
│   │   └── lifecycle.go         # transições de label
│   ├── github/                  # cliente REST, poller, labels, comments, PR
│   │   ├── client.go
│   │   ├── poller.go
│   │   └── pr.go
│   ├── telegram/                # webhook, comandos, envio de arquivos
│   │   ├── gateway.go
│   │   ├── commands.go
│   │   └── digest.go            # formatação do digest da spec
│   ├── runner/                  # invocação do Claude Code, stream-json, contexto
│   │   ├── runner.go
│   │   ├── streamjson.go        # parser dos eventos
│   │   ├── context.go           # cálculo de % da janela
│   │   └── resume.go            # encerramento + retomada via progress.md
│   ├── store/                   # SQLite (modernc.org/sqlite), migrations, queries
│   │   ├── store.go
│   │   ├── migrations.go
│   │   └── queries.go
│   ├── spec/                    # geração/leitura de docs/specs/issue-{N}/
│   │   └── files.go
│   └── domain/                  # tipos compartilhados (Issue, Task, Session, RepoState)
│       └── types.go
├── docs/
│   └── specs/                   # estas specs + specs por issue (issue-{N}/)
├── migrations/                  # arquivos .sql versionados
├── config.example.yaml
├── go.mod
└── CLAUDE.md
```

Convenção: pacotes em `internal/` para impedir importação externa; sem dependência circular (o `domain` não importa ninguém; `scheduler` orquestra os demais).

## 6. Fluxo fim-a-fim (caminho feliz)

1. Humano cria issue e aplica label `agent:ready`.
2. **Tick do Scheduler** (ex.: a cada 60s): GitHubPoller lista issues `agent:ready` nos repos ativos.
3. Scheduler registra a issue no SQLite (idempotente) e despacha ao TaskRunner com fase = `documentation`.
4. TaskRunner cria `docs/specs/issue-{N}/{design,tasks,progress,context}.md`, invoca o Claude Code para preencher as specs, monitorando contexto.
5. Ao concluir, muda a label para `documentation`, gera o **digest**, e o TelegramGateway envia digest + `.md` como anexos. Estado SQLite = `awaiting_approval`.
6. Humano revisa no celular e responde `/approve #N`.
7. TelegramGateway grava a aprovação; Scheduler move a label para `todo`.
8. Próximo tick: issue `todo` → TaskRunner muda label para `doing` e inicia a codificação (com checagem de contexto entre blocos de tasks).
9. Ao terminar: cria branch, abre PR, muda label para `done`, comenta na issue e notifica o Telegram com o link do PR.
10. Humano revisa e faz merge manual no GitHub.

## 7. Concorrência

- **Uma instância do Claude Code por tarefa** (subprocesso isolado), conforme requisito.
- O Scheduler limita o nº de tarefas simultâneas via semáforo configurável (`max_concurrent_tasks`, padrão a definir na validação — sugestão: 1 a 2 para começar).
- Cada tarefa adquire um **lock** no SQLite (por `repo+issue`) para evitar processamento duplo entre ticks.
- O webhook do Telegram roda em goroutine própria (servidor HTTP); a comunicação com o Scheduler é por canal/queue + escrita no SQLite (sem estado compartilhado mutável direto).

## 8. Controle de pausa por repo

- `/pause repo` e `/resume repo` gravam o estado em SQLite (`repo_state.paused`).
- O Scheduler **pula** repos pausados no início de cada tick (não enfileira novas tarefas). Tarefas já em execução naquele repo terminam normalmente (decisão a validar: drenar vs. abortar).

## 9. Gestão de janela de contexto (resumo)

Antes de avançar entre fases (`documentation → doing`) ou entre blocos de tasks, o TaskRunner calcula `uso = tokens_da_janela / janela_do_modelo`. Se `uso > 65%`:
1. Atualiza `progress.md`.
2. Encerra a sessão atual.
3. Abre nova sessão injetando `progress.md` + `context.md` como contexto inicial.

Detalhes (como os tokens são extraídos do `stream-json`) em `task_runner.md`.

## 10. Restrições técnicas (consolidadas)

- Go 1.22+.
- SQLite via `modernc.org/sqlite` (driver puro Go, **sem CGO**).
- GitHub via REST API com **fine-grained PAT**.
- Telegram via Bot API em modo **webhook** (não long polling).
- Claude Code invocado via `exec.Command` com `--dangerously-skip-permissions --output-format stream-json` (+ flags de não-interatividade — ver `task_runner.md`).
- Monitoramento de contexto a partir do output do Claude Code antes de cada avanço de fase.
- Modelo configurável por env/arquivo; padrão `claude-opus-4-5` (opusplan), com override por repo e por issue.

## 11. Observabilidade e operação (proposta)

- Logging estruturado (slog) com `repo`, `issue`, `session_id`, `phase`.
- Toda transição de estado e cada comando do Telegram são auditados no SQLite.
- `/status` retorna um resumo derivado do SQLite (repos, pausas, issues por fase, sessões ativas).

## 12. Decisões tomadas e pontos abertos

### Decisões confirmadas (validação 2026-06-15)

- **Modelo padrão:** `claude-opus-4-5` (opusplan), mantido conforme pedido. Override por repo/issue via config.
- **Local das specs por issue:** no **repo-alvo** (web/mobile/hybrid), junto do código gerado — versionadas no mesmo PR da feature.
- **Hospedagem:** **VM única no GCP Compute Engine**. O Argos roda inteiro nessa VM, com o webhook do Telegram na mesma máquina. **TLS via Caddy** (Let's Encrypt automático) como reverse proxy. Topologia: `Internet → Caddy → :8080 (servidor HTTP do Argos)`. Ver `telegram_gateway.md` §2.
- **`max_concurrent_tasks` inicial:** **1** (valor conservador dado o consumo de RAM do Claude Code CLI). Configurável para aumento futuro via `config.yaml`.
- **Comportamento de `/pause`:** **drenar** — a task em execução do repo termina normalmente; novas tasks daquele repo não são enfileiradas enquanto pausado.

### Topologia de deploy (VM única + Caddy)

O orquestrador é **stateful e long-running**, atendido por uma VM sempre ligada:
- **Processo único, sempre ligado**: Scheduler em loop + servidor HTTP do webhook + subprocessos `claude`, um só processo (gerido por `systemd`, restart automático).
- **Disco persistente por padrão** (disco da VM) para o SQLite e os checkouts dos repos-alvo — sem o problema de filesystem efêmero. Ver `persistence.md`.
- **Dependências na VM**: Claude Code CLI + `git` instalados no host.
- **Caddy** provê HTTPS público com renovação automática de certificado; só o Caddy fica exposto, o `:8080` permanece interno.

### Pontos ainda abertos

1. **Intervalo de polling** (sugestão: 60s) e backoff em rate limit.

## 13. Backlog

### `/preview` — preview de aplicações via browser — prioridade v1.1 (registrado 2026-06-15)

> Média-alta prioridade. Alvo: **v1.1**, logo após a primeira versão estável.

**Descrição:** permitir que o humano visualize o resultado do trabalho do agente no browser, sem precisar fazer deploy manual ou rodar o projeto localmente.

**Comandos:**
- `/preview #N` — sobe o servidor de dev do repo associado à issue N e retorna uma URL temporária acessível pelo celular.
- `/preview stop #N` — derruba o servidor de preview da issue N.
- `/preview status` — lista previews ativos.

**Comportamento esperado:**
- O Argos sobe o servidor de dev adequado ao tipo de repo:
  - `web` (Python + React): `uvicorn` + `npm run dev`.
  - `mobile` (Flutter): `flutter run -d web-server --web-port <porta>` (Flutter Web como aproximação visual).
  - `hybrid` (Flutter): idem `mobile`.
- Expõe a porta via ferramenta de tunelamento (ngrok, cloudflared ou porta aberta na VM — decisão de implementação, **não acoplada ao comando**).
- Retorna a URL no Telegram assim que o servidor estiver pronto.
- Preview é temporário: derruba automaticamente após timeout configurável (sugestão: 30 minutos) ou por comando explícito.

**Motivação:** validação visual rápida pelo celular sem ambiente local nem deploy manual. O comando é nomeado `/preview` (não `/ngrok`) para desacoplar a interface da ferramenta de tunelamento usada internamente.

**Restrições e pontos a decidir na implementação:**
- Gerenciar portas disponíveis na VM (evitar conflito entre previews simultâneos).
- Flutter Web é uma aproximação — não substitui teste em device físico para mobile.
- Timeout e limpeza automática de processos de preview órfãos.
- Autenticação da URL exposta (a avaliar — ngrok free não suporta senha).

### Conversa livre com o agente via Telegram (texto + imagens) — prioridade v1.2 (registrado 2026-06-15)

> Média prioridade. Alvo: **v1.2**.

**Descrição:** permitir que o humano envie mensagens de texto livre e imagens (prints de UI) diretamente pelo Telegram como instruções para o agente, sem precisar estruturar um comando formal.

**Exemplos de uso:**
- Enviar um print de tela e escrever "alinha o botão de submit com o campo de email".
- Enviar "muda a cor do header para o tom de laranja do design system".
- Enviar um print de erro e escrever "corrige esse bug".

**Comportamento esperado:**
- Mensagens fora do padrão `/comando` são tratadas como **instruções livres**.
- Imagens enviadas via Telegram são recebidas pelo Argos e repassadas como **contexto visual (vision)** para o Claude Code na sessão ativa ou na próxima sessão.
- Quando há mais de uma task ativa, o Argos **pergunta para qual issue/repo** a instrução se destina antes de repassar (roteamento de contexto).
- A instrução é registrada no `context.md` da issue correspondente para rastreabilidade.

**Motivação:** reduzir fricção nas correções rápidas de UI e bugs simples — o humano não precisa abrir o GitHub, criar uma issue formal e aplicar labels para pequenos ajustes visuais.

**Requisitos técnicos a resolver na implementação:**
- Download da imagem via Telegram Bot API (`getFile` + download).
- Repasse da imagem ao Claude Code com suporte a vision (**confirmar suporte no `stream-json`/CLI antes de implementar**).
- Roteamento de instrução quando múltiplas tasks estão ativas (menu de seleção no Telegram ou comando `/para #N <mensagem>`).
- Gestão de sessão: se não houver sessão ativa para o repo, enfileirar a instrução para a próxima execução via `context.md`.
