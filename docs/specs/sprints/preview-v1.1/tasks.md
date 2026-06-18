# Tasks — Sprint: `/preview` (v1.1)

> Decomposição da sprint em fases e tarefas atômicas. Cada tarefa deve resultar em código compilável e testável de forma isolada. Ordem dentro de cada fase é obrigatória; fases são sequenciais.

**Leia antes:** `design.md` (esta pasta) e `docs/specs/design.md` (arquitetura geral).

---

## Fase 1 — Fundação (schema + config + tipos)

> Sem lógica de negócio ainda. Objetivo: compilar, migrar o DB, e ter os tipos em mãos para as fases seguintes.

- [ ] **1.1 Migration `0002_previews.sql`**
  - Criar `migrations/0002_previews.sql` com o DDL da tabela `previews` (ver design §8).
  - Confirmar que o runner de migrations em `internal/store/migrations.go` aplica em ordem.
  - Verificar com `sqlite3` (ou teste) que a tabela é criada corretamente.

- [ ] **1.2 Tipos de domínio** (`internal/domain/types.go`)
  - Adicionar `Preview`, `PreviewStatus` (constantes: `starting|running|stopping|stopped|dead`), `PreviewStopReason`.
  - Não adicionar lógica — só structs e constantes.

- [ ] **1.3 Configuração** (`internal/config/config.go`)
  - Adicionar struct `PreviewConfig` com os campos do design §11.
  - Adicionar campo `Preview PreviewConfig` na struct `Config` raiz.
  - Adicionar defaults sensatos (ver design §10) quando o campo não está no YAML.
  - Atualizar `config.example.yaml` com a seção `preview:`.

- [ ] **1.4 Queries de persistência** (`internal/store/queries.go`)
  - Implementar as 7 queries listadas no design §8:
    - `CreatePreview`, `SetPreviewRunning`, `StopPreview`
    - `GetActivePreview`, `GetPreviewByIssue`
    - `MarkStalePreviewsDead`, `ListPreviews`
  - Escrever teste de integração cobrindo o ciclo: create → running → stopped.

---

## Fase 2 — PreviewManager (núcleo)

> Toda a lógica de orquestração de processos. Não depende do Telegram ainda — pode ser testado com stub de notificação.

- [ ] **2.1 PortManager** (`internal/preview/ports.go`)
  - Implementar `PortManager` com `Alloc()` e `Release()` (design §7).
  - `Alloc()` para repo `web` reserva duas portas consecutivas; demais reservam uma.
  - Thread-safe (`sync.Mutex`).
  - Teste unitário: alocar todas as portas do range, verificar erro, liberar uma, alocar novamente.

- [ ] **2.2 Tipos internos do preview** (`internal/preview/types.go`)
  - `DevServerConfig` por tipo de repo (comandos, working dir, porta).
  - `activePreview` — struct interna do manager (IDs de processo, timers, context cancel).

- [ ] **2.3 DevServer** (`internal/preview/devserver.go`)
  - `DevServer.Start(ctx, repo, checkoutDir, port) error`
  - Tabela de configuração por tipo de repo (design §5).
  - Healthcheck via HTTP GET com retry até `startup_timeout_seconds`.
  - Para `web`: iniciar `uvicorn` na porta `port+1` antes de `npm run dev` na porta `port`.
    - **Ponto aberto 14.1**: se o repo `web` não precisar de backend (SPA puro), pular uvicorn. Checar `vite.config.js` antes de codificar esta parte — registrar decisão em `context.md`.
  - Logar stdout/stderr dos subprocessos com `slog` (prefixo `repo=X issue=N`).

- [ ] **2.4 Tunnel (cloudflared)** (`internal/preview/tunnel.go`)
  - `Tunnel.Start(ctx, port) (url string, err error)`
  - `exec.CommandContext` com `cloudflared tunnel --url http://localhost:<port>`.
  - Ler stderr linha a linha; extrair URL por regexp `https://[a-z0-9\-]+\.trycloudflare\.com`.
  - Timeout de `startup_timeout_seconds` para a URL aparecer.
  - Goroutine de monitoramento: se o processo morrer → chamar callback de crash.

- [ ] **2.5 PreviewManager** (`internal/preview/manager.go`)
  - `PreviewManager.Start(ctx, issueID, repo, checkoutDir) error`
    - Derruba preview ativo anterior se existir (com notificação).
    - Orquestra DevServer → Tunnel → SQLite → Telegram → Timer.
  - `PreviewManager.Stop(issueID, reason) error`
    - Cancela context dos processos filhos, aguarda até 5s, libera porta, atualiza SQLite.
  - `PreviewManager.StatusAll() ([]PreviewInfo, error)`
    - Lê SQLite + calcula tempo restante em memória (expiresAt - now).
  - Timer de timeout: `time.AfterFunc(timeout, func() { Stop(issueID, "timeout") + Notify })`.
  - Tratamento de crash: goroutine do Tunnel/DevServer notifica via channel interno → Stop com reason `crash` + Notify.

- [ ] **2.6 Startup recovery** (`internal/preview/manager.go` ou `cmd/orchestrator/main.go`)
  - `PreviewManager.RecoverStale(ctx)`: chama `store.MarkStalePreviewsDead()` e notifica via Telegram para cada preview morto.
  - Chamado antes do Scheduler iniciar (ver design §12).

---

## Fase 3 — Integração com TelegramGateway

> Conecta os comandos Telegram ao PreviewManager. Depende da Fase 2 completa.

- [ ] **3.1 Parsing de comandos preview** (`internal/telegram/commands.go`)
  - Adicionar `PreviewCommand` e `PreviewAction` (design §9).
  - Ampliar o parser existente para reconhecer `/preview #N`, `/preview stop #N`, `/preview status`.
  - Validações básicas: issue N deve ser inteiro positivo.
  - Testes unitários de parsing (casos: start, stop, status, N sem `#`, argumento inválido).

- [ ] **3.2 Handlers de preview** (`internal/telegram/gateway.go`)
  - `handlePreviewStart(cmd)`: valida fase da issue (doing|done), chama `PreviewManager.Start`, trata erros com mensagem amigável.
  - `handlePreviewStop(cmd)`: chama `PreviewManager.Stop`, responde confirmação.
  - `handlePreviewStatus(cmd)`: chama `PreviewManager.StatusAll`, formata tabela e envia.
  - Despachar para os handlers a partir do switch de comandos existente.

- [ ] **3.3 Notificações novas** (`internal/telegram/gateway.go`)
  - Implementar as mensagens listadas no design §9 (tabela de eventos).
  - Expor métodos no `Gateway` interface para o `PreviewManager` chamar:
    - `NotifyPreviewReady(chatID, issueID, url, expiresMinutes)`.
    - `NotifyPreviewStopped(chatID, issueID, reason)`.
    - `NotifyPreviewReplaced(chatID, oldIssueID, newIssueID)`.
    - `NotifyPreviewError(chatID, issueID, err)`.

---

## Fase 4 — Wiring e validação

> Integra tudo no binário, testa o caminho feliz na VM e fecha a sprint.

- [ ] **4.1 Injeção no `main.go`**
  - Instanciar `PortManager`, `PreviewManager` no startup.
  - Chamar `RecoverStale` antes de iniciar o Scheduler.
  - Passar `PreviewManager` ao `TelegramGateway`.
  - Verificar que o binário compila (`CGO_ENABLED=0 go build ./cmd/orchestrator`).

- [ ] **4.2 Checklist de dependências na VM**
  - Documentar em `context.md` (esta pasta) como instalar `cloudflared`, `npm`/`node`, `flutter` no PATH do systemd.
  - Testar que `cloudflared tunnel --url http://localhost:9000` funciona na VM (teste manual antes do smoke test).

- [ ] **4.3 Smoke test manual**
  - Com um repo `web` ou `mobile` em checkout na VM:
    1. Enviar `/preview #N` via Telegram.
    2. Verificar que a URL chega no Telegram.
    3. Abrir a URL no celular — a aplicação deve carregar.
    4. Aguardar timeout ou enviar `/preview stop #N`.
    5. Verificar notificação de encerramento.
    6. Reiniciar o Argos com preview ativo → verificar notificação de `dead`.

- [ ] **4.4 Atualização de specs**
  - Atualizar `docs/specs/telegram_gateway.md` §5 com os novos comandos.
  - Atualizar `docs/specs/persistence.md` §3 com a tabela `previews`.
  - Atualizar `CLAUDE.md` (seção Comandos do Telegram + Dependências na VM).
  - Atualizar `docs/specs/design.md` §13 marcando `/preview` como implementado (mover de backlog para feature).

---

## Resumo de arquivos afetados / novos

| Arquivo | Operação |
| ------- | -------- |
| `migrations/0002_previews.sql` | **novo** |
| `internal/domain/types.go` | editar — adicionar tipos Preview |
| `internal/config/config.go` | editar — adicionar PreviewConfig |
| `config.example.yaml` | editar — adicionar seção preview |
| `internal/store/queries.go` | editar — 7 novas queries |
| `internal/store/migrations.go` | verificar — sem edição esperada |
| `internal/preview/` | **novo pacote** (5 arquivos) |
| `internal/telegram/commands.go` | editar — parsing preview |
| `internal/telegram/gateway.go` | editar — handlers + notificações |
| `cmd/orchestrator/main.go` | editar — wiring |
| `docs/specs/sprints/preview-v1.1/context.md` | criar durante a sprint |
| `docs/specs/telegram_gateway.md` | atualizar |
| `docs/specs/persistence.md` | atualizar |
| `CLAUDE.md` | atualizar |

## Critérios de aceite da sprint

1. `/preview #N` entrega URL HTTPS funcional no Telegram para repos `web`, `mobile` e `hybrid`.
2. `/preview stop #N` encerra o servidor e o túnel com confirmação.
3. `/preview status` lista corretamente (URL, tempo restante).
4. Timeout de 30 min encerra o preview e notifica.
5. Restart do Argos com preview ativo → notificação de `dead`, sem processo órfão.
6. Binário compila com `CGO_ENABLED=0` sem warnings.
7. Specs de `telegram_gateway.md` e `persistence.md` atualizadas.
