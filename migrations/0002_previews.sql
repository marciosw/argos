-- 0002_previews.sql — tabela de previews de aplicação (sprint /preview v1.1).
--
-- Registra o ciclo de vida de cada preview: processo de dev + túnel cloudflared.
-- Um preview por vez (max_previews=1); um novo substitui o anterior.
-- Ver docs/specs/sprints/preview-v1.1/design.md §8.

CREATE TABLE previews (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id    INTEGER NOT NULL REFERENCES issues(id),
    repo        TEXT NOT NULL,
    port        INTEGER NOT NULL,
    extra_port  INTEGER,              -- porta adicional (ex.: uvicorn p/ repo web)
    tunnel_url  TEXT,                 -- URL HTTPS do cloudflared (preenchida após start)
    -- starting | running | stopping | stopped | dead
    status      TEXT NOT NULL,
    started_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    stopped_at  TIMESTAMP,
    -- timeout | command | replaced | crash | restart
    stop_reason TEXT
);

CREATE INDEX idx_previews_issue ON previews (issue_id);
CREATE INDEX idx_previews_status ON previews (status);
