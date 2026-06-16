# progress.md — Issue 1 (Orchestrator / Argos)

Retomada entre sessões. Conciso e suficiente para reconstruir o estado sem o
transcript original (ver task_runner.md §6).

## Sessão 1 — concluída em 2026-06-15

### Concluído

- **`go.mod`** — módulo `github.com/marciomacedo/argos` (Go 1.26). Deps:
  `modernc.org/sqlite` (puro Go, sem CGO) e `gopkg.in/yaml.v3`.
- **`internal/domain/types.go`** — tipos compartilhados: `Issue`, `Session`,
  `RepoState`, `Approval`, `Lock`, `Command`, `Task`, `ModelConfig`,
  `AuditEntry`, `StatusSnapshot` + enums (`Phase`, `SessionStatus`,
  `ApprovalState`, `CommandType`) e constantes de repos/labels. O pacote não
  importa nenhum outro pacote interno (base do grafo de dependências).
- **`internal/store/`** — camada SQLite:
  - `store.go` — `Open` com PRAGMAs (`busy_timeout`, `foreign_keys`, `WAL` em
    arquivo), pool de 1 conexão (escritor serializado), helpers de timestamp.
  - `migrations.go` — aplica migrations embutidas em ordem, idempotente, via
    `schema_migrations`.
  - `queries.go` — todas as queries da persistence.md §4: `UpsertIssue`,
    `GetIssue`/`GetIssueByID`, `SetPhase`, `SetSpecDir`/`SetPRURL`/`SetLastError`,
    `AcquireLock`/`ReleaseLock`/`ReapExpiredLocks`,
    `CreateSession`/`SetClaudeSessionID`/`SetSessionContext`/`EndSession`/`GetSession`,
    `OpenApproval`/`DecideApproval`/`GetPendingApproval`,
    `SetRepoPaused`/`EnsureRepo`/`GetRepoState`/`ListActiveRepos`/`UpdatePollCursor`,
    `MarkEventProcessed`/`WasProcessed`, `Audit`, `StatusSnapshot`.
- **`migrations/0001_init.sql`** — schema completo (repo_state, issues, sessions,
  locks, approvals, processed_events, audit_log) + índices. `migrations/embed.go`
  expõe os `.sql` via `embed.FS` (mantém os arquivos na raiz e acessíveis ao
  pacote store).
- **`internal/config/`** — `config.go` (`Config` + `Load` + defaults; tokens só
  por env, nunca no YAML) e `model.go` (`ConfigManager.Resolve`/`Reload` com
  precedência **issue > repo-env > repo-file > env-global > arquivo > default**).
- **`config.example.yaml`** — exemplo comentado com todos os campos.
- **Testes**: `internal/store/store_test.go` (todas as queries em banco
  `:memory:` + um teste em arquivo confirmando WAL) e
  `internal/config/model_test.go` (precedência em todos os cenários, janelas,
  threshold via env, Load/Reload, defaults).

### Estado atual

- `CGO_ENABLED=0 go build ./...` → OK.
- `CGO_ENABLED=0 go test ./...` → **todos passando** (config + store).
- `go vet ./...` → limpo. `gofmt` aplicado.
- Banco inicializa e migrations rodam sem CGO (confirmado por teste em arquivo
  com `PRAGMA journal_mode = wal`).

### Decisões tomadas nesta sessão (registrar/validar)

1. **Módulo**: `github.com/marciomacedo/argos`.
2. **Pool de conexões**: `SetMaxOpenConns(1)` (escritor único serializado).
   Simples e robusto para `:memory:` e arquivo; revisar na Sessão 2 se for
   preciso 1 escritor + N leitores com WAL (persistence.md §9 ponto 4).
3. **Migrations embutidas** num pacote `migrations` na raiz (embed não alcança
   `../`), importado pelo store — `.sql` permanecem na raiz como pedido.
4. **`awaiting_approval` só no SQLite** (sem label dedicada) — conforme
   persistence.md §3.
5. **`EnsureRepo`**: `ListActiveRepos` só enxerga repos com linha em
   `repo_state`; o startup (Sessão 2+) deve chamar `EnsureRepo` para cada repo
   configurado.
6. **Precedência de modelo**: inseri *env por repo* (`ORCHESTRATOR_MODEL_<REPO>`)
   acima do override de arquivo do mesmo repo, e *issue override* acima de tudo,
   mantendo *repo > env-global > arquivo > default* da spec.
7. **`DecideApproval` não muda a fase da issue** — a transição de label/fase é do
   Scheduler (design.md §3).

### Sessão 2 — por onde começar

- **Primeiro arquivo a abrir**: `docs/specs/github_poller.md` + `design.md` §5
  (estrutura de pacotes). Implementar `internal/github/` (client.go, poller.go,
  pr.go) — cliente `net/http` próprio (recomendação da spec; sem `go-github`).
- Em seguida, `internal/scheduler/` (loop + lifecycle de labels), consumindo
  store + config já prontos.
- **Decisões pendentes a confirmar antes de codificar o runner/poller**:
  - Flags reais do Claude Code CLI e schema do `stream-json` (`claude --help`) —
    task_runner.md §11.
  - Intervalo de polling/backoff (design.md §12 ponto 1; default 60s já no config).
  - Lib do GitHub: confirmar `net/http` próprio (github_poller.md §10 ponto 1).
  - Local dos checkouts dos repos-alvo (worktrees vs. clones) — task_runner.md §11.
- **Pontos de atenção**:
  - Manter `CGO_ENABLED=0` em todo build/CI.
  - Reconciliação labels↔SQLite é do Scheduler; o poller só executa operações de
    GitHub (github_poller.md §1).
  - Inconsistência de doc: persistence.md §30 e telegram_gateway.md ainda citam
    Cloud Run/efêmero em parte do texto, mas a decisão confirmada é **VM única no
    GCP Compute Engine com disco persistente** (design.md §12). Atualizar essas
    seções da spec quando conveniente.
  - `go test -race` exige CGO; rodar testes sem `-race` para honrar `CGO_ENABLED=0`.

### Modelo recomendado por sessão

As specs já estão detalhadas e a Sessão 1 fixou os padrões (store, config,
domain, estilo de erro/log/teste), então a maior parte do que resta é "seguir o
padrão", adequado ao Sonnet. A exceção é o **runner**, onde há decisões de
julgamento e incerteza real (schema do `stream-json` a confirmar, cálculo de %
de janela, ciclo de vida do subprocesso, reset de sessão) — manter no Opus.

| Pacote / sessão | Complexidade | Modelo sugerido |
| --- | --- | --- |
| `internal/github/` (client REST, poller, PR) | mecânico (HTTP + tipos) | **Sonnet** |
| `internal/telegram/` (webhook, parsing de comandos) | mecânico (parsing + HTTP) | **Sonnet** |
| `internal/scheduler/` (loop + máquina de estados, locks) | média (reconciliação labels↔SQLite, idempotência, concorrência) | **Sonnet** (Opus se surgir ambiguidade) |
| `internal/runner/` (subprocesso `claude`, `stream-json`, janela de contexto, retomada) | **alta** (specs pedem confirmar contra o CLI instalado) | **Opus** |

Recomendação prática: Sonnet para github → telegram → scheduler; trocar para
Opus no runner ou em qualquer sessão que abra uma decisão ainda não resolvida na
spec. Para o Sonnet render bem, manter prompts de sessão com escopo fechado e
restrições explícitas (como nesta Sessão 1).
