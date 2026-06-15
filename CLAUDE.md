# CLAUDE.md — Orchestrator

Contexto do projeto para agentes (Claude Code) e colaboradores. Leia antes de codificar.

## O que é

**orchestrator** — orquestrador em Go que gerencia agentes de codificação autônomos. Faz polling de GitHub Issues em 3 repositórios, despacha cada tarefa para o Claude Code CLI como subprocesso, monitora o uso da janela de contexto, persiste estado em SQLite e usa um bot do Telegram como canal de comando e validação humana.

Repositórios gerenciados:
- `web` — Python + React
- `mobile` — Flutter
- `hybrid` — Flutter (web + mobile)

## Estado atual

🚧 **Fase de specs.** Nenhum código de produção ainda. As specs em `docs/specs/` precisam ser **validadas pelo humano** antes de qualquer implementação.

Ordem de leitura das specs:
1. [docs/specs/design.md](docs/specs/design.md) — arquitetura geral, componentes, pacotes Go, pontos abertos.
2. [docs/specs/github_poller.md](docs/specs/github_poller.md) — polling de issues e gestão de labels.
3. [docs/specs/telegram_gateway.md](docs/specs/telegram_gateway.md) — webhook, comandos, envio de arquivos.
4. [docs/specs/task_runner.md](docs/specs/task_runner.md) — invocação do Claude Code, contexto, retomada.
5. [docs/specs/persistence.md](docs/specs/persistence.md) — schema SQLite e controle de estado.
6. [docs/specs/model_config.md](docs/specs/model_config.md) — configuração de modelo.

## Fluxo de trabalho (spec-driven)

Toda issue segue o ciclo de labels:

```
agent:ready → documentation → todo → doing → done
```

- `agent:ready` → agente cria specs em `docs/specs/issue-{N}/` e muda para `documentation`.
- `documentation` → agente elabora specs; ao concluir, envia digest + `.md` no Telegram e **aguarda aprovação humana**.
- `todo` → aprovação recebida (`/approve #N`); o agente pega no próximo polling e muda para `doing`.
- `doing` → codificação em andamento.
- `done` → PR aberto; agente comenta na issue e notifica o Telegram com o link.

### Dois portões de validação humana
1. **Fim da documentação**: revisão da spec pelo Telegram (`/approve #N` ou `/reject #N motivo`).
2. **PR aberto**: review e merge manuais no GitHub.

### Spec por issue: `docs/specs/issue-{N}/`
- `design.md` — arquitetura e decisões técnicas da feature.
- `tasks.md` — tarefas decompostas (sprint).
- `progress.md` — estado atual (feito / onde parou / próximo passo); atualizado ao fim de cada sessão para retomada.
- `context.md` — objetivo de negócio, restrições, referências, decisões do humano (inclui motivos de `/reject`).

## Comandos do Telegram

| Comando             | Efeito |
| ------------------- | ------ |
| `/approve #N`       | Aprova a spec da issue N → label `todo`. |
| `/reject #N motivo` | Rejeita a spec → volta para `documentation` com o motivo em `context.md`. |
| `/pause repo`       | Pausa o repo (`web`/`mobile`/`hybrid`). |
| `/resume repo`      | Retoma o repo. |
| `/status`           | Status de todos os repos e issues em andamento. |
| `/run #N`           | Força o processamento imediato da issue N. |

## Convenções

- **Go 1.22+**. Pacotes internos em `internal/` (ver árvore em `design.md`).
- **SQLite via `modernc.org/sqlite`** (puro Go, **sem CGO** → `CGO_ENABLED=0`).
- **GitHub** via REST API com **fine-grained PAT** (escopo mínimo por repo).
- **Telegram** via Bot API em **webhook** (não long polling).
- **Claude Code** chamado via `exec.Command` com `--dangerously-skip-permissions --output-format stream-json` (+ flags de não-interatividade; ver `task_runner.md`).
- **Branch protection**: agentes só criam branches (`agent/issue-{N}`) e abrem PRs — **nunca** push direto na `main`.
- **Segredos sempre via env**: `GITHUB_TOKEN`, `TELEGRAM_BOT_TOKEN`, `TELEGRAM_WEBHOOK_SECRET`. Nunca versionar.
- **Logging estruturado** (`log/slog`) com `repo`, `issue`, `session_id`, `phase`.
- **Idempotência**: eventos do GitHub/Telegram processados uma única vez (ver `persistence.md`).

## Gestão de janela de contexto

Antes de avançar entre fases ou blocos de tasks, o TaskRunner calcula `uso = tokens_da_janela / janela_do_modelo`. Se `> 65%`: atualiza `progress.md`, encerra a sessão e inicia uma nova com `progress.md` + `context.md` injetados. Detalhes em `task_runner.md`.

## Configuração de modelo

Configurável por env/arquivo, com override por repo/issue. Default do projeto: `claude-opus-4-5` (opusplan). **Ponto em aberto**: avaliar `claude-opus-4-8` como default (ver `model_config.md`). Precedência: issue > repo > env > arquivo > default.

## Comandos úteis (planejados — ainda não implementados)

```bash
# build (binário estático, sem CGO)
CGO_ENABLED=0 go build ./cmd/orchestrator

# testes
go test ./...

# rodar
./orchestrator --config config.yaml

# verificar flags reais do Claude Code (necessário antes de implementar o runner)
claude --help
```

## Decisões e pontos em aberto

Decisões confirmadas (2026-06-15):
- Modelo padrão: `claude-opus-4-5` (opusplan).
- Specs por issue: no **repo-alvo**, junto do código.
- Hospedagem: **VM única no GCP Compute Engine** (Argos + webhook na mesma VM). TLS via **Caddy** (Let's Encrypt automático), reverse proxy para `:8080`. Disco da VM persistente — sem filesystem efêmero.
- `max_concurrent_tasks` inicial: **1** (consumo de RAM do Claude Code CLI); configurável via `config.yaml`.
- `/pause`: **drenar** — task em execução termina; novas tasks do repo não são enfileiradas enquanto pausado.

Pontos em aberto (ver `docs/specs/design.md` §12):
1. Intervalo de polling e backoff.

## Backlog

### `/preview` — preview de aplicações via browser — prioridade v1.1 (registrado 2026-06-15)

Média-alta prioridade, alvo **v1.1** (logo após a primeira versão estável). Permite ao humano visualizar o resultado do trabalho do agente no browser/celular, sem deploy manual nem ambiente local.

- `/preview #N` — sobe o servidor de dev do repo da issue N e retorna URL temporária.
- `/preview stop #N` — derruba o preview da issue N.
- `/preview status` — lista previews ativos.

Servidor de dev por tipo de repo: `web` → `uvicorn` + `npm run dev`; `mobile`/`hybrid` → `flutter run -d web-server --web-port <porta>` (Flutter Web como aproximação). Exposição via tunelamento (ngrok/cloudflared/porta na VM — decisão de implementação, desacoplada do comando). Preview temporário: timeout configurável (sugestão 30 min) ou comando explícito. Nomeado `/preview` (não `/ngrok`) para desacoplar a interface da ferramenta.

Pontos a decidir na implementação: gestão de portas na VM, limpeza de processos órfãos, autenticação da URL exposta (ngrok free não suporta senha), e a ressalva de que Flutter Web não substitui teste em device físico. Detalhes em `docs/specs/design.md` §13.

### Conversa livre com o agente via Telegram (texto + imagens) — prioridade v1.2 (registrado 2026-06-15)

Média prioridade, alvo **v1.2**. Permite enviar texto livre e imagens (prints de UI) pelo Telegram como instruções, sem comando formal — ex.: print de tela + "alinha o botão de submit com o campo de email".

- Mensagens fora do padrão `/comando` viram instruções livres.
- Imagens repassadas como contexto visual (vision) ao Claude Code, na sessão ativa ou na próxima.
- Com múltiplas tasks ativas, o Argos pergunta a qual issue/repo a instrução se destina (ou via `/para #N <mensagem>`).
- Instrução registrada no `context.md` da issue para rastreabilidade.

Pontos a resolver na implementação: download de imagem via Telegram (`getFile`), suporte a vision no `stream-json`/CLI (a confirmar), roteamento entre tasks ativas, e enfileiramento via `context.md` quando não há sessão ativa. Detalhes em `docs/specs/design.md` §13 e `docs/specs/telegram_gateway.md` §12.

> Ao trabalhar neste repo: **não comece a codificar antes de as specs serem validadas**. Atualize a spec relevante quando uma decisão for tomada e registre no `audit_log`/commit.
