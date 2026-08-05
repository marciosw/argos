-- 0001_init.sql — schema inicial do orchestrator (Argos).
--
-- Fonte de verdade do estado OPERACIONAL do orquestrador (as labels da issue no
-- GitHub continuam sendo a verdade do estado de NEGÓCIO; o Scheduler reconcilia
-- os dois a cada tick). Ver docs/specs/persistence.md.
--
-- Driver: modernc.org/sqlite (puro Go, sem CGO). Timestamps são gravados como
-- TEXT no formato 'YYYY-MM-DD HH:MM:SS' (UTC), via CURRENT_TIMESTAMP ou pela
-- aplicação; a camada Go faz o parse/format (ver internal/store).

-- Estado por repositório (pausa, cursor de polling).
CREATE TABLE repo_state (
    repo            TEXT PRIMARY KEY,                 -- 'web' | 'mobile' | 'hybrid'
    paused          INTEGER NOT NULL DEFAULT 0,
    paused_reason   TEXT,
    last_polled_at  TIMESTAMP,
    etag            TEXT,                             -- cache condicional do GitHub
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Uma linha por issue conhecida pelo orquestrador.
CREATE TABLE issues (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    repo            TEXT NOT NULL,
    number          INTEGER NOT NULL,
    title           TEXT,
    -- ready|documentation|awaiting_approval|todo|doing|done|error
    phase           TEXT NOT NULL,
    spec_dir        TEXT,                             -- docs/specs/issue-{N}/
    github_url      TEXT,
    pr_url          TEXT,
    last_error      TEXT,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (repo, number)
);

CREATE INDEX idx_issues_phase ON issues (phase);

-- Cada execução de subprocesso `claude` (uma por tarefa; várias por issue se
-- houver retomada por janela de contexto).
CREATE TABLE sessions (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id            INTEGER NOT NULL REFERENCES issues(id),
    phase               TEXT NOT NULL,                -- documentation | doing
    claude_session_id   TEXT,                         -- session_id do stream-json
    model               TEXT NOT NULL,
    status              TEXT NOT NULL,                -- running|completed|failed|resumed
    context_pct         REAL,                         -- último uso de janela (0..1)
    resumed_from        INTEGER REFERENCES sessions(id),
    started_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ended_at            TIMESTAMP
);

CREATE INDEX idx_sessions_issue ON sessions (issue_id);
CREATE INDEX idx_sessions_status ON sessions (status);

-- Lock de processamento por issue (evita despacho duplo entre ticks). O lease
-- (expires_at) permite recuperação após crash via ReapExpiredLocks.
CREATE TABLE locks (
    issue_id    INTEGER PRIMARY KEY REFERENCES issues(id),
    holder      TEXT NOT NULL,                        -- id da goroutine/worker
    acquired_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at  TIMESTAMP
);

-- Fila de aprovações (validação humana ao fim da documentação).
CREATE TABLE approvals (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id     INTEGER NOT NULL REFERENCES issues(id),
    state        TEXT NOT NULL,                       -- pending | approved | rejected
    reason       TEXT,                                -- motivo do /reject
    requested_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    decided_at   TIMESTAMP,
    decided_by   INTEGER                              -- chat_id do Telegram
);

CREATE INDEX idx_approvals_issue_state ON approvals (issue_id, state);

-- Idempotência de eventos externos (updates do Telegram, comentários do GitHub).
CREATE TABLE processed_events (
    source       TEXT NOT NULL,                       -- 'telegram' | 'github'
    external_id  TEXT NOT NULL,                       -- update_id, comment_id, etc.
    processed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (source, external_id)
);

-- Auditoria de transições e comandos.
CREATE TABLE audit_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id    INTEGER REFERENCES issues(id),
    repo        TEXT,
    event       TEXT NOT NULL,                        -- 'phase_change'|'command'|'error'...
    detail      TEXT,                                 -- JSON livre
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_audit_issue ON audit_log (issue_id);
