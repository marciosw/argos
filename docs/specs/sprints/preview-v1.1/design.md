# Design — Sprint: `/preview` (v1.1)

> Spec de implementação do comando `/preview` no Argos: permite ao humano subir um servidor de dev do repo-alvo e receber uma URL temporária no Telegram para validação visual pelo celular, sem deploy manual nem ambiente local.

**Status:** rascunho — aguardando validação humana antes de codificar.

**Decisões tomadas (2026-06-17):**
- Tunelamento: **cloudflared** (modo rápido, sem conta).
- Autenticação: **URL obscura, sem auth** (suficiente para uso pessoal v1.1).
- Concorrência: **máximo 1 preview ativo** — novo `/preview` derruba o anterior com aviso.
- Restart: previews marcados como `running` no startup → marcados como `dead` + notificação.

---

## 1. Objetivo

Permitir que o humano execute `/preview #N` no Telegram e receba, em segundos, uma URL HTTPS temporária que abre o resultado do trabalho do agente na issue N — sem precisar de ambiente local, acesso SSH à VM, nem deploy manual.

## 2. Comandos

| Comando              | Efeito                                                                          |
| -------------------- | ------------------------------------------------------------------------------- |
| `/preview #N`        | Sobe servidor de dev da issue N e retorna URL via Telegram. Derruba preview anterior se existir. |
| `/preview stop #N`   | Derruba o servidor de preview da issue N imediatamente.                          |
| `/preview status`    | Lista previews ativos (issue, repo, URL, tempo restante).                        |

### Restrições de estado
- `/preview #N` só aceita issues nas fases `doing` ou `done` (a fase `documentation` não tem código para executar).
- Issue N precisa ter um checkout local em disco (feito pelo bootstrap do workspace, sessão 6).

## 3. Arquitetura — novo componente `preview`

```
                    ┌──────────────────────────────────────────┐
                    │            PreviewManager                 │
                    │                                           │
  TelegramGateway ──▶  start(issueID) ──▶  PortManager        │
  (commandos)      │  stop(issueID)         │                  │
                    │  statusAll()     ┌─────▼──────┐          │
                    │                  │ DevServer  │          │
                    │                  │ (por repo) │          │
                    │                  └─────┬──────┘          │
                    │                        │ localhost:PORT   │
                    │                  ┌─────▼──────┐          │
                    │                  │  Tunnel    │          │
                    │                  │(cloudflared│          │
                    │                  └─────┬──────┘          │
                    │                        │ HTTPS URL        │
                    │                  ┌─────▼──────┐          │
                    │                  │  Timeout   │          │
                    │                  │  Timer     │          │
                    │                  └────────────┘          │
                    └──────────────────────────────────────────┘
                              │
                    ┌─────────▼──────────┐
                    │  Persistence (DB)  │
                    │  tabela previews   │
                    └────────────────────┘
```

### Pacote Go novo: `internal/preview/`

```
internal/preview/
├── manager.go      # PreviewManager: orquestração, start/stop/status, timeout
├── devserver.go    # lança processo(s) de dev por tipo de repo
├── tunnel.go       # lança cloudflared, extrai URL do stdout
├── ports.go        # PortManager: aloca/libera portas no range configurado
└── types.go        # Preview, PreviewStatus, PreviewAction, DevServerConfig
```

## 4. Fluxo de `/preview #N`

```
Telegram → TelegramGateway.handlePreview(N)
  1. Valida issue N existe + fase in {doing, done}
  2. Se preview ativo: chama stop(atual) + notifica "Derrubando preview anterior da issue M"
  3. PortManager.Alloc() → porta livre no range [port_range_start, port_range_end]
  4. INSERT previews(status='starting') no SQLite
  5. DevServer.Start(repo, checkoutDir, porta) → aguarda processo pronto (healthcheck ou timeout)
  6. Tunnel.Start(porta) → lê stdout até encontrar URL (timeout: startup_timeout_seconds)
  7. UPDATE previews(status='running', tunnel_url=URL)
  8. Telegram.Notify(chatID, "✅ Preview da issue #N: <URL>\nExpira em 30 min")
  9. TimeoutTimer.Reset(timeout_minutes) — goroutine que chama stop() ao expirar
```

### Fluxo de `/preview stop #N`
```
  1. Lê preview ativo da issue N do SQLite
  2. Cancela context do devserver + tunnel (SIGTERM → SIGKILL após 5s)
  3. PortManager.Release(porta)
  4. UPDATE previews(status='stopped', stop_reason='command')
  5. Telegram.Notify("🛑 Preview da issue #N encerrado")
```

### Fluxo de timeout automático
```
  Timer dispara após timeout_minutes:
  1. Chama stop(issueID) com stop_reason='timeout'
  2. Telegram.Notify("⏱️ Preview da issue #N expirou (30 min)")
```

### Startup recovery
```
  No startup do Argos (antes do Scheduler iniciar):
  1. SELECT previews WHERE status IN ('starting', 'running', 'stopping')
  2. Para cada um: UPDATE status='dead', stop_reason='restart'
  3. Telegram.Notify("⚠️ Preview da issue #N marcado como morto (Argos reiniciou)")
```

## 5. DevServer por tipo de repo

| Repo      | Processo(s)                                              | Porta exposta         |
| --------- | -------------------------------------------------------- | --------------------- |
| `web`     | `npm run dev` (React, porta `P`) + `uvicorn` (porta `P+1`) | `P` (frontend)     |
| `mobile`  | `flutter run -d web-server --web-port P`                 | `P`                   |
| `hybrid`  | `flutter run -d web-server --web-port P`                 | `P`                   |

**Nota `web`:** o frontend React (`npm run dev`) é exposto no preview — não o backend. O backend (`uvicorn`) é iniciado em porta adjacente (`P+1`) caso o frontend precise dele para funcionar (proxy reverso no `vite.config.js`). Se o frontend for SPA puro (sem proxy), só o `npm run dev` é necessário. Registrar essa decisão no `context.md` ao iniciar a sprint.

**Working directory:** checkout local do repo-alvo, gerenciado pelo workspace (sessão 6). Caminho: `<workspace_dir>/<repo>/`.

**Healthcheck:** para verificar que o servidor está pronto, tentar `GET http://localhost:P` com retry por até `startup_timeout_seconds`. Retorno de qualquer HTTP status ≠ conexão recusada é suficiente.

## 6. Cloudflared (tunelamento)

**Modo rápido (sem conta/token):**
```bash
cloudflared tunnel --url http://localhost:<PORT>
```

- Gera URL aleatória HTTPS (`https://<random>.trycloudflare.com`) e a imprime no stderr.
- O `Tunnel.Start()` lê o stderr linha a linha procurando a URL (`regexp: https://[a-z0-9-]+\.trycloudflare\.com`).
- Timeout de `startup_timeout_seconds` para a URL aparecer; falha com erro claro se não aparecer.
- Processo filho do Argos: `exec.CommandContext` com context cancelável.
- cloudflared deve estar instalado na VM como dependência do sistema.

**Limitações conhecidas (v1.1):**
- URL muda a cada preview (não é URL fixa).
- Sem autenticação — a URL é secreta por obscuridade.
- cloudflared free pode ter rate limiting em uso intenso (improvável para uso pessoal).

## 7. Gestão de portas

```go
type PortManager struct {
    start, end int        // range configurado
    inUse      map[int]struct{}
    mu         sync.Mutex
}

func (p *PortManager) Alloc() (int, error)    // primeira porta livre no range
func (p *PortManager) Release(port int)        // libera porta
```

- Range default: `9000–9099` (configurável via `preview.port_range_start/end`).
- `web` usa 2 portas (`P` e `P+1`); os outros usam 1. `Alloc()` para `web` reserva `P` e `P+1` de vez.
- Com `max_previews=1`, raramente haverá mais de 2 portas em uso simultâneo.

## 8. Schema SQLite — nova tabela `previews`

Migration: `migrations/0002_previews.sql`

```sql
CREATE TABLE previews (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id    INTEGER NOT NULL REFERENCES issues(id),
    repo        TEXT NOT NULL,
    port        INTEGER NOT NULL,
    extra_port  INTEGER,                -- porta adicional (ex.: uvicorn para web)
    tunnel_url  TEXT,
    status      TEXT NOT NULL,          -- starting | running | stopping | stopped | dead
    started_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    stopped_at  TIMESTAMP,
    stop_reason TEXT                    -- timeout | command | replaced | crash | restart
);
```

### Queries novas (em `internal/store/`)

| Query | Descrição |
| ----- | --------- |
| `CreatePreview(issueID, repo, port, extraPort)` | INSERT status='starting' |
| `SetPreviewRunning(id, tunnelURL)` | UPDATE status='running' |
| `StopPreview(id, reason)` | UPDATE status='stopped/dead', stopped_at=now |
| `GetActivePreview()` | SELECT WHERE status IN ('starting','running','stopping') — espera-se 0 ou 1 |
| `GetPreviewByIssue(issueID)` | Para o stop por issue |
| `MarkStalePreviewsDead()` | Startup recovery: todos running/starting → dead |
| `ListPreviews(statusFilter)` | Para /preview status |

## 9. Adições ao TelegramGateway

### Novos tipos de comando

```go
// Em internal/telegram/commands.go
type PreviewAction string
const (
    PreviewStart  PreviewAction = "start"
    PreviewStop   PreviewAction = "stop"
    PreviewStatus PreviewAction = "status"
)

type PreviewCommand struct {
    Action  PreviewAction
    IssueID int  // para start/stop; 0 para status
}
```

### Parsing

Ampliar o parser de comandos existente:

```
/preview #N          → PreviewCommand{Action: start, IssueID: N}
/preview stop #N     → PreviewCommand{Action: stop,  IssueID: N}
/preview status      → PreviewCommand{Action: status}
```

### Notificações novas (em `internal/telegram/gateway.go`)

| Evento                            | Mensagem                                                                |
| --------------------------------- | ----------------------------------------------------------------------- |
| Preview iniciando                 | "⏳ Subindo preview da issue #N (`repo`)..."                            |
| Preview pronto                    | "✅ Preview da issue #N: `<URL>`\nExpira em `X` min"                   |
| Preview substituído               | "⚠️ Preview anterior da issue #M encerrado para abrir #N"              |
| Preview encerrado por comando     | "🛑 Preview da issue #N encerrado"                                      |
| Preview expirado por timeout      | "⏱️ Preview da issue #N expirou (`X` min)"                             |
| Preview morto por crash           | "💥 Preview da issue #N caiu inesperadamente"                           |
| Preview morto no restart          | "⚠️ Preview da issue #N marcado como morto (Argos reiniciou)"          |
| `/preview status` (lista)         | tabela Markdown com issue, repo, URL, tempo restante                    |
| Erro ao iniciar                   | "❌ Falha ao iniciar preview da issue #N: `<motivo>`"                  |

## 10. Configuração

Adições ao `config.yaml` / `ConfigManager`:

```yaml
preview:
  enabled: true
  port_range_start: 9000
  port_range_end: 9099
  timeout_minutes: 30
  startup_timeout_seconds: 30
  cloudflared_bin: "cloudflared"    # path do binário (default: no PATH)
```

## 11. Adições ao `internal/config/`

```go
type PreviewConfig struct {
    Enabled               bool   `yaml:"enabled"`
    PortRangeStart        int    `yaml:"port_range_start"`
    PortRangeEnd          int    `yaml:"port_range_end"`
    TimeoutMinutes        int    `yaml:"timeout_minutes"`
    StartupTimeoutSeconds int    `yaml:"startup_timeout_seconds"`
    CloudflaredBin        string `yaml:"cloudflared_bin"`
}
```

## 12. Wiring no `cmd/orchestrator/main.go`

```
startup:
  1. Persistence.MarkStalePreviewsDead() → notificações via Telegram
  2. PortManager = NewPortManager(cfg.Preview)
  3. PreviewManager = NewPreviewManager(store, portMgr, gateway, cfg.Preview)
  4. Gateway.RegisterPreviewHandler(previewMgr)
```

## 13. Dependências externas na VM

| Dependência       | Instalação sugerida                       | Obrigatória? |
| ----------------- | ----------------------------------------- | ------------ |
| `cloudflared`     | `curl -L ... | bash` ou package do Cloudflare | Sim       |
| `npm` / `node`    | `nvm` ou package manager da VM           | Para repo `web` |
| `uvicorn` / `pip` | venv do projeto `web`                     | Para repo `web` |
| `flutter`         | Flutter SDK instalado no PATH             | Para `mobile`/`hybrid` |

> Esses binários precisam estar no PATH do processo Argos (gerenciado pelo systemd). Configurar o `Environment=` no unit do systemd conforme necessário.

## 14. Pontos abertos (a resolver durante a sprint)

1. **Frontend `web` com proxy**: confirmar se o `vite.config.js` do repo `web` tem proxy para o backend — isso determina se é necessário subir o `uvicorn`. Checar antes de implementar o `DevServer` para `web`.
2. **Healthcheck de Flutter Web**: `flutter run` demora mais para iniciar (~10–20s). Avaliar se `startup_timeout_seconds=30` é suficiente ou aumentar para 60s.
3. **Sinal de término do cloudflared**: testar se SIGTERM é suficiente ou se é necessário SIGKILL imediato.
4. **Número da issue vs. ID interno**: os comandos do Telegram usam o número da issue no GitHub (`#N`), não o `id` interno do SQLite. Confirmar que `GetIssue(repo, number)` já existe na store (ver `persistence.md` §4 — sim, `GetIssue(repo, number)` está previsto). O repo precisa ser inferido do contexto (um preview por vez torna isso trivial) ou explicitado no comando.

## 15. Fora de escopo (v1.1)

- Autenticação da URL exposta.
- URL fixa/persistente entre previews.
- Preview de múltiplos repos simultaneamente.
- Hot-reload ou restart automático do servidor de dev.
- Suporte a mobile nativo (device físico) — Flutter Web é a aproximação adotada.
