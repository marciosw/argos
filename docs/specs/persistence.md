# Spec — Persistence (SQLite)

> Camada de persistência via **`modernc.org/sqlite`** (driver puro Go, sem CGO). Fonte de verdade do estado operacional do orquestrador: evita reprocessamento, rastreia status, controla locks, pausas e auditoria.

**Status:** rascunho para validação.

---

## 1. Responsabilidades

1. Persistir o estado operacional: issues conhecidas, fase, sessões, locks.
2. Garantir **idempotência** (eventos do GitHub e do Telegram processados uma única vez).
3. Controlar a **fila de aprovações** (estado `awaiting_approval`).
4. Persistir **pausa por repo**.
5. Auditar transições de estado e comandos.
6. Suportar `/status` com leituras agregadas.

> Divisão de responsabilidade: as **labels da issue** são a verdade do estado de negócio; o **SQLite** é a verdade do estado operacional. O Scheduler reconcilia os dois a cada tick.

## 2. Driver e configuração

- `modernc.org/sqlite` (sem CGO) → binário estático, build simples (`CGO_ENABLED=0`).
- Conexão única ou pool pequeno. SQLite serializa escritas; usar:
  - `PRAGMA journal_mode=WAL;` (concorrência leitor/escritor).
  - `PRAGMA busy_timeout=5000;` (espera em vez de erro `SQLITE_BUSY`).
  - `PRAGMA foreign_keys=ON;`
- Caminho do arquivo configurável (`db_path`, default `./orchestrator.db`).
- **Migrations** versionadas (arquivos `.sql` em `migrations/`, aplicadas no startup; tabela `schema_migrations` controla a versão).

### ⚠️ Persistência no Cloud Run

A hospedagem definida é o GCP Cloud Run, cujo **filesystem é efêmero** — um arquivo SQLite no disco local **se perde** a cada nova instância/redeploy. Para preservar o estado:
- **Disco persistente montado** (volume) é a opção preferível: mantém a semântica de lock de arquivo que o SQLite exige. `db_path` deve apontar para o mount.
- **GCS FUSE é desaconselhado** para o arquivo SQLite (semântica de lock fraca → corrupção/`SQLITE_BUSY`).
- Combinar com **single-instance** (`max-instances=1`): SQLite não suporta múltiplos escritores em processos/instâncias distintas.

Ver `telegram_gateway.md` §2 e `design.md` §12 para a topologia completa.

## 3. Schema (proposta)

### `repo_state`
Estado por repositório (pausa, cursor de polling).

```sql
CREATE TABLE repo_state (
    repo            TEXT PRIMARY KEY,         -- 'web' | 'mobile' | 'hybrid'
    paused          INTEGER NOT NULL DEFAULT 0,
    paused_reason   TEXT,
    last_polled_at  TIMESTAMP,
    etag            TEXT,                      -- cache condicional do GitHub
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

### `issues`
Uma linha por issue conhecida pelo orquestrador.

```sql
CREATE TABLE issues (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    repo            TEXT NOT NULL,
    number          INTEGER NOT NULL,
    title           TEXT,
    phase           TEXT NOT NULL,            -- ready|documentation|awaiting_approval|todo|doing|done|error
    spec_dir        TEXT,                     -- docs/specs/issue-{N}/
    github_url      TEXT,
    pr_url          TEXT,
    last_error      TEXT,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (repo, number)
);
```

> Nota: `awaiting_approval` é um estado operacional **dentro** do label `documentation` no GitHub — não há label própria para ele (a label só muda para `todo` na aprovação).

### `sessions`
Cada execução de subprocesso `claude` (uma por tarefa; várias por issue se houver retomada).

```sql
CREATE TABLE sessions (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id            INTEGER NOT NULL REFERENCES issues(id),
    phase               TEXT NOT NULL,         -- documentation | doing
    claude_session_id   TEXT,                  -- session_id reportado pelo stream-json
    model               TEXT NOT NULL,
    status              TEXT NOT NULL,         -- running | completed | failed | resumed
    context_pct         REAL,                  -- último uso de janela observado (0..1)
    resumed_from        INTEGER REFERENCES sessions(id),  -- se reiniciada por contexto
    started_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ended_at            TIMESTAMP
);
```

### `locks`
Lock de processamento por issue (evita despacho duplo entre ticks).

```sql
CREATE TABLE locks (
    issue_id    INTEGER PRIMARY KEY REFERENCES issues(id),
    holder      TEXT NOT NULL,                 -- id da goroutine/worker
    acquired_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at  TIMESTAMP                      -- lease; lock expira p/ evitar deadlock
);
```

### `approvals`
Fila de aprovações (validação humana ao fim da documentação).

```sql
CREATE TABLE approvals (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id    INTEGER NOT NULL REFERENCES issues(id),
    state       TEXT NOT NULL,                 -- pending | approved | rejected
    reason      TEXT,                          -- motivo do /reject
    requested_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    decided_at  TIMESTAMP,
    decided_by  INTEGER                        -- chat_id do Telegram
);
```

### `processed_events`
Idempotência de eventos externos (updates do Telegram, comentários/edições do GitHub).

```sql
CREATE TABLE processed_events (
    source      TEXT NOT NULL,                 -- 'telegram' | 'github'
    external_id TEXT NOT NULL,                 -- update_id, comment_id, etc.
    processed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (source, external_id)
);
```

### `audit_log`
Auditoria de transições e comandos.

```sql
CREATE TABLE audit_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id    INTEGER REFERENCES issues(id),
    repo        TEXT,
    event       TEXT NOT NULL,                 -- 'phase_change', 'command', 'error', ...
    detail      TEXT,                          -- JSON livre
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

### `previews`
Ciclo de vida de cada preview de aplicação (`/preview`, sprint v1.1): processo de dev + túnel cloudflared. Um preview ativo por vez (`max_previews=1`); um novo substitui o anterior. Migration `0002_previews.sql`. Ver [sprints/preview-v1.1/design.md](sprints/preview-v1.1/design.md) §8.

```sql
CREATE TABLE previews (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id    INTEGER NOT NULL REFERENCES issues(id),
    repo        TEXT NOT NULL,
    port        INTEGER NOT NULL,
    extra_port  INTEGER,              -- porta adicional (ex.: uvicorn p/ repo web)
    tunnel_url  TEXT,                 -- URL HTTPS do cloudflared (preenchida após start)
    status      TEXT NOT NULL,        -- starting | running | stopping | stopped | dead
    started_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    stopped_at  TIMESTAMP,
    stop_reason TEXT                  -- timeout | command | replaced | crash | restart
);

CREATE INDEX idx_previews_issue ON previews (issue_id);
CREATE INDEX idx_previews_status ON previews (status);
```

> No startup, previews em estado transitório (`starting`/`running`/`stopping`) são marcados como `dead` (recovery — sem processo sobreviveu ao restart) e o humano é notificado via Telegram.

## 4. Queries principais

| Operação | Descrição |
| -------- | --------- |
| `UpsertIssue(repo, number, ...)` | Registra/atualiza issue (idempotente via `UNIQUE(repo, number)`). |
| `GetIssue(repo, number)` | Lê estado operacional da issue. |
| `SetPhase(issueID, phase)` | Transição de fase + `audit_log`. |
| `AcquireLock(issueID, holder, lease)` | `INSERT` condicional; falha se já existe lock vivo. Usado antes de despachar. |
| `ReleaseLock(issueID)` | Remove lock ao fim/erro da tarefa. |
| `ReapExpiredLocks(now)` | Limpa locks com `expires_at < now` (recuperação de crash). |
| `CreateSession(...)` / `EndSession(...)` | Ciclo de vida das sessões + `context_pct`. |
| `OpenApproval(issueID)` | Cria `approvals(pending)` e marca issue como `awaiting_approval`. |
| `DecideApproval(issueID, approved, reason, chatID)` | Resolve aprovação (`approved`/`rejected`). |
| `SetRepoPaused(repo, bool, reason)` | Pausa/retoma repo. |
| `ListActiveRepos()` | Repos não pausados (para o poller). |
| `MarkEventProcessed(source, id)` / `WasProcessed(source, id)` | Idempotência. |
| `StatusSnapshot()` | Agregação para `/status` (repos, pausas, contagem por fase, sessões ativas). |

## 5. Controle de estado e idempotência

- **Locks com lease**: cada lock tem `expires_at`. Se o orquestrador cair, locks expiram e são recuperados no próximo `ReapExpiredLocks`. Tarefas órfãs são reconciliadas (issue volta a um estado recuperável).
- **Idempotência de comandos do Telegram**: `update_id` gravado em `processed_events` antes de agir; updates repetidos são ignorados.
- **Idempotência de polling**: antes de despachar, checar fase + lock; a mesma issue não é processada duas vezes na mesma fase.
- **Transições atômicas**: mudança de fase + auditoria numa transação; quando envolve GitHub (label), a ordem é: aplicar label no GitHub → confirmar → gravar fase no SQLite (com reconciliação se divergir).

## 6. Concorrência

- WAL + `busy_timeout` cobrem a concorrência típica (poller, webhook, runners).
- Escritas críticas (lock, transição) em transações curtas.
- Evitar transações longas durante chamadas de rede/subprocesso (segurar dados em memória e persistir em transações curtas).

## 7. Migrations e versionamento

- `migrations/0001_init.sql`, `0002_*.sql`, ... aplicadas em ordem no startup.
- Tabela `schema_migrations(version, applied_at)`.
- Sem downgrade automático; mudanças destrutivas exigem migration explícita.

## 8. Backup / retenção (proposta)

- DB único em arquivo; backup = cópia do arquivo (com WAL checkpoint) ou `VACUUM INTO`.
- `audit_log` e `processed_events` podem crescer — política de retenção/limpeza a definir (ex.: manter 90 dias).

## 9. Pontos abertos para validação

1. Confirmar o conjunto de estados (`phase`) e se `awaiting_approval` fica só no SQLite (sem label dedicada).
2. Duração do lease do lock (sugestão: 2× o timeout de fase).
3. Política de retenção de `audit_log`/`processed_events`.
4. Pool de conexões: única conexão de escrita serializada vs. pool (sugestão: 1 escritor + N leitores com WAL).
