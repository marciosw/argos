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

## Sessão 2 — concluída em 2026-06-15

### Concluído

- **`internal/github/client.go`** — `Client` sobre `net/http` (sem dependência pesada):
  - Headers `Authorization: Bearer`, `Accept: application/vnd.github+json`, `X-GitHub-Api-Version: 2022-11-28`.
  - Timeout de 30 s por request.
  - Retry com backoff exponencial em 5xx e 429 (`maxRetries = 4`).
  - Leitura de `X-RateLimit-Remaining/Limit/Reset`; respeito ao `Retry-After` em 429.
  - `WithInstantBackoff()` para testes sem sleeps reais.
- **`internal/github/poller.go`** — `Poller` interface + implementação (`poller`):
  - `NewPollerFromConfig` lê o token da env nomeada em `cfg.TokenEnv` (nunca hardcode).
  - `ListByLabels` — GET issues abertas com qualquer das labels (OR), `per_page=100`.
  - `TransitionLabel` — reconciliação: GET labels → diff → PUT (substituição atômica); noop se já no estado correto.
  - `SetLabel` / `ClearLabel` — idem via reconciliação (idempotente).
  - `Comment` — POST comment na issue.
  - Tipos: `Issue`, `OpenPRInput`, `PullRequest` (brutos do GitHub; separados de `domain.Issue`).
- **`internal/github/pr.go`** — `OpenPR`:
  - Confirma existência da branch via `GET /git/ref/heads/{branch}` antes de criar o PR.
  - `POST /pulls` com title, head, base, body.
  - Log estruturado com slog (`repo`, `issue`, `pr_number`, `url`).
- **`internal/github/poller_test.go`** — 13 testes com `httptest.Server` (sem chamadas de rede reais):
  - `TestListByLabels`, `TestListByLabelsEmpty`, `TestListByLabelsUnknownRepo`.
  - `TestTransitionLabel`, `TestTransitionLabelNoop`, `TestComputeTransition`.
  - `TestRetry429`, `TestRetry429WithRetryAfter`, `TestRetry5xx`, `TestRetryExhausted`, `TestNo4xxRetry`.
  - `TestRateLimitHeaders`.
  - `TestOpenPR`, `TestOpenPRBranchNotFound`.

### Estado atual

- `CGO_ENABLED=0 go build ./...` → OK.
- `CGO_ENABLED=0 go test ./...` → **todos passando** (config + store + github).
- `go vet ./...` → limpo.
- Token nunca logado; lido do ambiente via `os.Getenv(cfg.TokenEnv)`.

## Sessão 3 — concluída em 2026-06-16

### Concluído

- **`internal/domain/types.go`** — adicionado campo `Phase domain.Phase` a `Task` (única alteração permitida no domain nesta sessão).
- **`internal/scheduler/runner.go`** — interface `TaskRunner` que desacopla o scheduler do runner real (Sessão 4+).
- **`internal/scheduler/scheduler.go`** — `Scheduler` struct + `New` + `Run` (loop com ticker + canal de comandos + `ctx.Done`) + `processTick` (ReapExpiredLocks + ListActiveRepos + processRepo por repo ativo) + `handleCommand` (CmdApprove, CmdReject, CmdPause, CmdResume; CmdStatus/CmdRun como stubs logados) + `findIssue` (busca em todos os repos configurados).
- **`internal/scheduler/lifecycle.go`** — `processRepo` (ListByLabels filtrando LabelReady/LabelTodo), `handleReady` (UpsertIssue → AcquireLock → TransitionLabel ready→documentation → SetPhase → semáforo → goroutine → SetPhase awaiting_approval ou error + ReleaseLock), `handleTodo` (AcquireLock → TransitionLabel todo→doing → SetPhase → semáforo → goroutine → TransitionLabel doing→done + SetPhase done ou error + ReleaseLock).
- **`internal/scheduler/scheduler_test.go`** — 8 testes com fakes (sem biblioteca de mock):
  - `TestHandleReady`: agent:ready → TransitionLabel chamado + runner invocado com Phase=documentation.
  - `TestHandleTodo`: todo → runner invocado com Phase=doing.
  - `TestRepoPausado`: repo pausado → processRepo não despachado.
  - `TestLockJaExistente`: lock pré-existente → skip sem dispatch.
  - `TestSemaforoLimita`: max_concurrent_tasks=1 → segunda issue bloqueia até primeira terminar.
  - `TestCmdApprove`: /approve #N → SetPhase(todo) + TransitionLabel.
  - `TestCmdPause`: /pause repo → SetRepoPaused(true).
  - `TestListByLabelsErro`: ListByLabels com erro → sem panic.

### Estado atual

- `CGO_ENABLED=0 go build ./...` → OK.
- `CGO_ENABLED=0 go test ./...` → **todos passando** (config + domain + github + scheduler + store).

### Decisões tomadas nesta sessão

1. Semáforo adquirido **no caller** (antes de lançar a goroutine), liberado **dentro da goroutine** — o número de goroutines lançadas já reflete o limite.
2. `handleTodo` faz UpsertIssue quando a issue não está no store, para tolerância a tick de polling que chega antes da issue ter sido registrada via `handleReady`.
3. `findIssue` itera `cfg.GitHub.Repos` (sem ordem garantida em map Go) — OK porque os números de issue são únicos por repo neste projeto.

## Sessão 4 — concluída em 2026-06-16

### Concluído

- **`internal/telegram/digest.go`** — `DigestData`, `ApprovalRequest`, `FormatDigest` (HTML).
- **`internal/telegram/commands.go`** — `parseCommand` puro: strip `@botname`, aceita `#N`/`N`, valida repo em `/pause`/`/resume`, `motivo` livre em `/reject`, erro descritivo em comando desconhecido ou args faltando.
- **`internal/telegram/gateway.go`** — `tgClient` (setWebhook, sendMessage, sendDocument multipart) + `Gateway` interface + `gateway` struct:
  - `New` — lê tokens das envs configuradas; retorna erro se ausentes (nunca loga).
  - `newWithToken` — construtor interno para testes (base URL sobrescrita pelo httptest.Server).
  - `Start` — registra webhook (pula se `WebhookURL == ""`), serve `POST /telegram/webhook` em `:8080`, faz `Shutdown` ao cancelar ctx.
  - Handler webhook: valida secret (→ 403), verifica `AllowedChatIDs`, idempotência por `update_id` (SQLite), responde 200 antes de goroutine, `/status` tratado direto via `store.StatusSnapshot`, demais comandos enviados ao canal (non-blocking).
  - `SendApprovalRequest`, `Notify`, `NotifyAll`, `NotifyPR`, `NotifyError`.
- **`internal/telegram/telegram_test.go`** — 16 testes:
  - `parseCommand`: `/approve #42`, `/approve 42`, `/approve@bot #42`, `/reject #1 texto longo`, `/reject #1` (erro), `/pause web`, `/pause invalid` (erro), `/status`, comando desconhecido (erro).
  - `FormatDigest`: campos esperados + instrução de aprovação no HTML.
  - `tgClient`: path e payload de `sendMessage`; multipart de `sendDocument`.
  - Handler webhook: secret inválido → 403; chat não autorizado → 200 + descartado; `update_id` duplicado → 200 + descartado; comando válido → `domain.Command` no canal.

### Estado atual

- `CGO_ENABLED=0 go build ./...` → OK.
- `CGO_ENABLED=0 go test ./...` → **todos passando** (config + domain + github + scheduler + store + telegram).

### Decisões tomadas nesta sessão

1. `newWithToken` exposto no pacote para testes internos (base URL do tgClient injetável via httptest.Server); `New` é o construtor público que lê as envs.
2. `/status` resolvido diretamente no webhook handler (goroutine chama `store.StatusSnapshot` e `sendMessage`) — não passa pelo canal do Scheduler.
3. Handler retorna 200 antes de qualquer I/O pesado; goroutine usa `context.Background()` desacoplado do request.
4. `sendDocument` usa `multipart/form-data`; em falha de leitura de arquivo, loga e continua com os demais (digest já enviado).

## Sessão 5 — concluída em 2026-06-16

### Portão de verificação (`claude --help` + run real de `stream-json`)

Executado contra o CLI instalado (`claude` 2.1.178). **Flags da spec §2 confirmadas
como reais**: `-p/--print`, `--model`, `--output-format stream-json`, `--verbose`,
`--dangerously-skip-permissions`, `--add-dir`. Sem divergência de flags.

Schema real do `stream-json` (run mínimo `echo prompt | claude -p --output-format
stream-json --verbose`):

- `{"type":"system","subtype":"init","session_id":...,"model":...,"tools":[...]}` —
  traz `session_id` no nível superior; **sem `usage`**.
- `{"type":"rate_limit_event",...}` — evento extra, tolerado/ignorado pelo parser.
- `{"type":"assistant","message":{...,"usage":{...}},...}` — **o `usage` por turno vem
  ANINHADO em `message.usage`**, não no nível superior (divergência vs. spec §3, que
  o desenhava no topo do `StreamEvent`). Adotado o schema real: o parser lê
  `message.usage` (assistant) e `usage` (result).
- `{"type":"result","subtype":"success","is_error":false,"usage":{...},"total_cost_usd":...,
  "modelUsage":{"<model>":{"contextWindow":200000,...}},...}` — `usage` no topo, igual
  ao último turno; `is_error` discrimina falha; há até `modelUsage.<model>.contextWindow`
  (não usado — a janela vem do ConfigManager).

Campos de `Usage` da spec confirmados: `input_tokens`, `output_tokens`,
`cache_read_input_tokens`, `cache_creation_input_tokens`. O fallback de §4 (sem `usage`)
não foi necessário. A divergência (usage aninhado) **não muda o design**, só o mapeamento.

### Concluído

- **`internal/runner/exec.go`** — `commandRunner` (interface injetável) + `execRunner`
  real: `exec.CommandContext` com `cmd.Dir`, prompt via **stdin**, `bufio.Scanner` com
  buffer 8 MiB (linhas JSON grandes), stderr capturado, `Cancel`=SIGTERM + `WaitDelay`
  (SIGKILL após carência). Exit ≠ 0 volta em `commandResult.ExitCode` (não como erro Go).
- **`internal/runner/stream.go`** — `StreamEvent`/`streamMessage`/`Usage`, `streamParser`
  (lê linha a linha, discrimina por `type`, captura `session_id` só p/ correlação, acumula
  o último `usage` de `message.usage`|`usage`, tolera linhas malformadas, marca `sawResult`),
  e `contextUsage(u, window) = (input+cache_read+cache_creation)/window`.
- **`internal/runner/prompts.go`** — `text/template` versionados: documentação, codificação
  e retomada (injeta `progress.md`+`context.md`).
- **`internal/runner/digest.go`** — extração best-effort do `DigestData` a partir das seções
  de `design.md` (Objetivos/Componentes/Decisões/Riscos), com placeholders se ausentes.
- **`internal/runner/git.go`** — `gitOps` (interface) + `execGit`: `EnsureBranch`, `Push`
  (`-u origin`, sem `--force`), `CountCommits` (`rev-list --count base..branch`).
- **`internal/runner/runner.go`** — `Runner` + `New`/`newWithDeps` + `Run` (despacha por
  `task.Phase`). `runDocumentation`: scaffold de specs, loop de sessões, `SetSpecDir`,
  digest → `SendApprovalRequest`. `runCoding`: `EnsureBranch`, loop, `Push`, `OpenPR`,
  `SetPRURL`, `Comment` na issue, `NotifyPR`. `runPhaseLoop`: cria sessão no store, roda
  subprocesso, parseia, calcula uso; `uso > limiar` → encerra sessão (`resumed`), audita
  `resumed_due_to_context`, abre **nova** sessão (reset deliberado, sem `--resume`); cap
  `maxSessions` (8) evita loop infinito. Erros (exit≠0, sem `result`, `is_error`) →
  `EndSession(failed)` + `NotifyError` + erro propagado, `progress.md` preservado.
- **`internal/runner/runner_test.go`** — 8 testes com fakes (sem `claude` real): parser
  (acúmulo de usage, malformado tolerado, sem `result`), `contextUsage` (abaixo/acima de
  0.65), `Run` documentation (digest+arquivos+sessão+SpecDir), `Run` doing (branch/push/PR/
  comentário/NotifyPR/PRURL), reinício por contexto (2 sessões, `resumed_from`,
  `resumed_due_to_context`), erro exit≠0 (erro propagado, `progress.md` preservado,
  `NotifyError`, sessão `failed`).

### Estado atual

- `CGO_ENABLED=0 go build ./...` → OK.
- `CGO_ENABLED=0 go test ./...` → **todos passando** (config + domain + github + runner +
  scheduler + store + telegram). `go vet ./...` limpo, `gofmt` aplicado.

### Decisões tomadas nesta sessão

1. **Efeitos de fim de fase DENTRO do runner** (confirmado contra `lifecycle.go`): `Run`
   retorna só `error`, mantendo `scheduler.TaskRunner` intacto. O digest de aprovação
   (documentation) e o PR+notificação (doing) acontecem antes de `Run` retornar; o
   scheduler então faz as transições de label/fase a partir do sucesso/erro.
2. **Prompt via stdin** (não `-p`), evitando o limite de tamanho de argumento (spec §11.2).
   `--add-dir` dispensado: `cmd.Dir` = checkout já dá acesso ao cwd.
3. **"Bloco de tasks" = uma invocação do subprocesso** (heurística simples, §11.3): ao
   término de cada invocação checa-se o uso; abaixo do limiar = fase concluída no orçamento,
   acima = retomada com contexto reiniciado. Cap `maxSessions=8` (constante no pacote, sem
   campo novo na config) evita loop infinito; ao esgotar, prossegue com aviso (o humano
   ainda gateia via aprovação/PR).
4. **Checkout do repo-alvo**: `task.RepoPath` assumido resolvido pelo caller (fallback `.`).
   **Pendência**: o scheduler ainda NÃO popula `RepoPath` (Task criada sem ele em
   `lifecycle.go`); uma sessão futura precisa fiar a resolução de checkout (clone/worktree)
   sem alterar o contrato congelado. Clone/worktree não implementados nesta sessão.
5. **`context.md` semeado mínimo**: o runner cria o scaffold vazio; a semeadura com o corpo
   da issue (spec §6) depende de o caller passar o body (Task não o carrega hoje) —
   pendência registrada.
6. Dependências injetadas por interfaces no pacote runner (`commandRunner`, `gitOps`,
   `prClient`, `notifier`); `github.Poller` e `telegram.Gateway` as satisfazem. Store
   concreto (`:memory:` real nos testes, como no scheduler). Tokens/usage nunca logados.

## Sessão 6 — concluída em 2026-06-16

### Concluído

- **`internal/workspace/`** (novo pacote) — gerência dos checkouts locais dos
  repos-alvo no disco persistente da VM (design §12):
  - `workspace.go` — `Manager` interface (`Prepare(ctx, repo) (path, err)`) +
    impl `manager`. `Prepare` é idempotente: clona em `<base_dir>/<repo>` se
    ausente (depois `checkout` da branch base); se presente, `fetch` +
    `checkout` + `reset --hard origin/<base>`. Devolve caminho ABSOLUTO. URL de
    clone derivada de `cfg.GitHub.Repos[repo]` (owner/name) — https com token
    `x-access-token` (token nunca logado; só `repo`/`path`/`branch` em log).
    `New(Options)` (git real) + `newWithGit` (fake nos testes).
  - `git.go` — `gitClient` (interface injetável: Clone/Fetch/Checkout/ResetHard)
    + `execGit` real sobre o binário git (mesmo padrão de `internal/runner/git.go`).
  - `workspace_test.go` — 6 testes com fake de git: clone quando ausente,
    fetch/checkout/reset quando presente, caminho absoluto correto, idempotência,
    repo desconhecido → erro, URL de clone sem token.
- **`internal/runner/runner.go`** (mudança ADITIVA): `Runner` ganhou
  `ws workspacePreparer` (interface local, padrão das demais deps); `New` recebe
  o `ws` como último parâmetro. `Run` chama `r.ws.Prepare(ctx, task.Repo)` no
  início e atribui o resultado a `task.RepoPath` (fia o checkout sem tocar o
  scheduler/`domain.Task`). Falha de `Prepare` → `NotifyError` + erro. Mantido o
  fallback `.` quando `ws == nil`.
  - `seedContext` — na fase `documentation`, semeia `context.md` com o corpo da
    issue (via novo `gh.GetIssue`) **só se** o arquivo estiver vazio (preserva
    motivos de `/reject`). Best-effort: falhas logadas, não abortam a fase.
- **`internal/github/poller.go`** (aditivo): `GetIssue(ctx, repo, issue)
  (Issue, error)` na interface `Poller` + impl (GET `/issues/{n}`, traz `Body`).
- **`internal/config/config.go`** — novos campos: `log_level`, `log_format` e
  bloco `workspace` (`base_dir`, `git_host`, `clone_scheme`) + defaults
  (`info`/`text`/`./checkouts`/`github.com`/`https`). `config.example.yaml`
  atualizado.
- **`cmd/orchestrator/main.go`** (novo) — bootstrap long-running: flag
  `--config`, `config.Load`, `slog` (texto/JSON + nível configurável),
  `store.Open` + `EnsureRepo` por repo, `github.NewPollerFromConfig`, canal
  `cmds` (buffer 32), `telegram.New`, `workspace.New` (token lido da env, nunca
  logado), `runner.New`, `scheduler.New`. `signal.NotifyContext`
  (SIGINT/SIGTERM) → cancela ctx; `gateway.Start` e `scheduler.Run` em
  goroutines; shutdown aguarda ambas via `sync.WaitGroup` com teto de 30s.
- **Testes do runner** (aditivos): `TestRunUsesWorkspacePath` (Prepare chamado +
  RepoPath usado como `cmd.Dir`), `TestRunSeedsContext` (corpo da issue em
  context.md), `TestSeedContextPreservesExisting` (não sobrescreve conteúdo
  prévio). Fakes existentes ganharam `GetIssue` e `fakeWorkspace`.

### Estado atual

- `CGO_ENABLED=0 go build ./...` → OK. `CGO_ENABLED=0 go test ./...` → **todos
  passando** (config + domain + github + runner + scheduler + store + telegram +
  workspace). `go vet ./...` limpo, `gofmt` aplicado.
- Smoke test do binário: sobe (`migration applied` + `em execução`), recebe
  SIGTERM e encerra com `encerrado de forma graciosa` (exit 0). Teste com
  `claude`/rede reais não executado (sem tokens/CLI no ambiente de CI).

### Decisões tomadas nesta sessão

1. **RepoPath fiado no runner** (opção recomendada): o `Runner` possui um
   `workspacePreparer`; resolve o checkout no início de `Run` e popula
   `task.RepoPath` localmente. **Nenhum pacote congelado alterado** —
   `scheduler`/`domain.Task` intactos; mudança em `runner.New` é aditiva
   (só os testes do próprio runner chamavam `New`/`newWithDeps`).
2. **Esquema de clone**: **https + token** (`x-access-token:<token>@host`), token
   lido da env `github.token_env` (mesma do API client). Evita gestão de chaves
   SSH na VM. `ssh` declarado como não suportado nesta versão. Layout da base:
   `<base_dir>/<repo>` (subpasta por repo lógico). Branch base mantida atualizada
   a cada `Prepare`.
3. **Semeadura de context.md**: o runner busca o corpo via novo
   `github.Poller.GetIssue` na fase `documentation` e escreve em `context.md`
   **apenas se vazio** (preserva motivos de `/reject`). Best-effort.
4. **Shutdown**: `signal.NotifyContext` cancela o ctx compartilhado;
   `gateway.Start` faz `srv.Shutdown`, `scheduler.Run` retorna; `main` aguarda as
   duas goroutines via `WaitGroup` com teto de 30s (log de warn se estourar).
   Tasks em voo recebem o ctx cancelado (subprocesso `claude` → SIGTERM).

## Sessão 5 — por onde começar

- **Pacote a implementar**: `internal/runner/` (task_runner.md).
- **Decisões pendentes antes de codificar**:
  - Verificar flags reais do Claude Code CLI: `claude --help` e schema do `stream-json` — task_runner.md §11.
  - Local dos checkouts dos repos-alvo (worktrees vs. clones simples) — task_runner.md §11.
- **Pontos de atenção**:
  - Cálculo de `uso = tokens_usados / janela_do_modelo`; threshold 65% em `ContextThreshold`.
  - Ao superar o threshold: atualizar `progress.md`, encerrar sessão, iniciar nova com `progress.md` + `context.md` injetados.
  - `exec.Command` com `--dangerously-skip-permissions --output-format stream-json`; confirmar flags exatas contra o CLI instalado.
  - `go test -race` exige CGO; rodar testes sem `-race` para honrar `CGO_ENABLED=0`.
- **Modelo sugerido**: Opus (alta complexidade, decisões de julgamento, schema a confirmar).

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
