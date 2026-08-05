package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/marciomacedo/argos/internal/domain"
)

// ============================ repo_state =============================

// EnsureRepo garante que exista uma linha em repo_state para o repo (não
// pausado por padrão). Deve ser chamado no startup para cada repo configurado,
// para que ListActiveRepos os enxergue. Idempotente.
func (s *Store) EnsureRepo(ctx context.Context, repo string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO repo_state (repo, paused) VALUES (?, 0)
		 ON CONFLICT(repo) DO NOTHING`, repo)
	if err != nil {
		return fmt.Errorf("store: ensure repo %q: %w", repo, err)
	}
	return nil
}

// SetRepoPaused pausa/retoma um repo (upsert), gravando o motivo.
func (s *Store) SetRepoPaused(ctx context.Context, repo string, paused bool, reason string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO repo_state (repo, paused, paused_reason, updated_at)
		 VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(repo) DO UPDATE SET
		   paused = excluded.paused,
		   paused_reason = excluded.paused_reason,
		   updated_at = CURRENT_TIMESTAMP`,
		repo, boolToInt(paused), nullStr(reason))
	if err != nil {
		return fmt.Errorf("store: set repo paused %q: %w", repo, err)
	}
	return nil
}

// UpdatePollCursor atualiza o cursor de polling (last_polled_at + ETag).
func (s *Store) UpdatePollCursor(ctx context.Context, repo string, polledAt time.Time, etag string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO repo_state (repo, last_polled_at, etag, updated_at)
		 VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(repo) DO UPDATE SET
		   last_polled_at = excluded.last_polled_at,
		   etag = excluded.etag,
		   updated_at = CURRENT_TIMESTAMP`,
		repo, formatTS(polledAt), nullStr(etag))
	if err != nil {
		return fmt.Errorf("store: update poll cursor %q: %w", repo, err)
	}
	return nil
}

// GetRepoState lê o estado de um repo. Retorna ErrNotFound se ausente.
func (s *Store) GetRepoState(ctx context.Context, repo string) (domain.RepoState, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT repo, paused, paused_reason, last_polled_at, etag, updated_at
		 FROM repo_state WHERE repo = ?`, repo)
	return scanRepoState(row)
}

// ListActiveRepos retorna os repos não pausados (para o poller). Só enxerga
// repos com linha em repo_state — ver EnsureRepo.
func (s *Store) ListActiveRepos(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT repo FROM repo_state WHERE paused = 0 ORDER BY repo`)
	if err != nil {
		return nil, fmt.Errorf("store: list active repos: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ============================== issues ===============================

// UpsertIssue registra (ou atualiza) uma issue de forma idempotente via
// UNIQUE(repo, number). Em conflito atualiza title/github_url, preservando a
// `phase` (esta só muda por SetPhase). Retorna o id da issue.
func (s *Store) UpsertIssue(ctx context.Context, repo string, number int, title, githubURL string, initialPhase domain.Phase) (int64, error) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO issues (repo, number, title, phase, github_url)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(repo, number) DO UPDATE SET
		   title = excluded.title,
		   github_url = excluded.github_url,
		   updated_at = CURRENT_TIMESTAMP`,
		repo, number, nullStr(title), string(initialPhase), nullStr(githubURL))
	if err != nil {
		return 0, fmt.Errorf("store: upsert issue %s#%d: %w", repo, number, err)
	}
	var id int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT id FROM issues WHERE repo = ? AND number = ?`, repo, number,
	).Scan(&id); err != nil {
		return 0, fmt.Errorf("store: upsert issue %s#%d (id): %w", repo, number, err)
	}
	return id, nil
}

// GetIssue lê o estado operacional de uma issue. ErrNotFound se ausente.
func (s *Store) GetIssue(ctx context.Context, repo string, number int) (domain.Issue, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, repo, number, title, phase, spec_dir, github_url, pr_url,
		        last_error, created_at, updated_at
		 FROM issues WHERE repo = ? AND number = ?`, repo, number)
	return scanIssue(row)
}

// GetIssueByID lê uma issue pelo id. ErrNotFound se ausente.
func (s *Store) GetIssueByID(ctx context.Context, id int64) (domain.Issue, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, repo, number, title, phase, spec_dir, github_url, pr_url,
		        last_error, created_at, updated_at
		 FROM issues WHERE id = ?`, id)
	return scanIssue(row)
}

// SetPhase aplica a transição de fase + registra em audit_log, numa transação.
func (s *Store) SetPhase(ctx context.Context, issueID int64, phase domain.Phase) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE issues SET phase = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			string(phase), issueID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return auditTx(ctx, tx, issueID, "", "phase_change",
			fmt.Sprintf(`{"phase":%q}`, phase))
	})
}

// SetSpecDir grava o diretório de specs da issue.
func (s *Store) SetSpecDir(ctx context.Context, issueID int64, specDir string) error {
	return s.execAffect(ctx,
		`UPDATE issues SET spec_dir = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		nullStr(specDir), issueID)
}

// SetPRURL grava a URL do PR aberto para a issue.
func (s *Store) SetPRURL(ctx context.Context, issueID int64, prURL string) error {
	return s.execAffect(ctx,
		`UPDATE issues SET pr_url = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		nullStr(prURL), issueID)
}

// SetLastError grava (ou limpa, com string vazia) o último erro da issue.
func (s *Store) SetLastError(ctx context.Context, issueID int64, msg string) error {
	return s.execAffect(ctx,
		`UPDATE issues SET last_error = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		nullStr(msg), issueID)
}

// =============================== locks ===============================

// AcquireLock tenta adquirir o lock da issue por um lease. Retorna false (sem
// erro) se já houver um lock vivo (não expirado). Usado antes de despachar.
func (s *Store) AcquireLock(ctx context.Context, issueID int64, holder string, lease time.Duration) (bool, error) {
	expires := formatTS(time.Now().Add(lease))
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO locks (issue_id, holder, acquired_at, expires_at)
		 VALUES (?, ?, CURRENT_TIMESTAMP, ?)
		 ON CONFLICT(issue_id) DO UPDATE SET
		   holder = excluded.holder,
		   acquired_at = CURRENT_TIMESTAMP,
		   expires_at = excluded.expires_at
		 WHERE locks.expires_at IS NOT NULL
		   AND locks.expires_at < CURRENT_TIMESTAMP`,
		issueID, holder, expires)
	if err != nil {
		return false, fmt.Errorf("store: acquire lock %d: %w", issueID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ReleaseLock remove o lock da issue (no fim/erro da tarefa).
func (s *Store) ReleaseLock(ctx context.Context, issueID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM locks WHERE issue_id = ?`, issueID)
	if err != nil {
		return fmt.Errorf("store: release lock %d: %w", issueID, err)
	}
	return nil
}

// ReapExpiredLocks remove locks expirados (recuperação após crash) e retorna
// quantos foram removidos.
func (s *Store) ReapExpiredLocks(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM locks WHERE expires_at IS NOT NULL AND expires_at < ?`,
		formatTS(now))
	if err != nil {
		return 0, fmt.Errorf("store: reap locks: %w", err)
	}
	return res.RowsAffected()
}

// ============================== sessions =============================

// SessionInput descreve a criação de uma sessão.
type SessionInput struct {
	IssueID         int64
	Phase           domain.Phase
	Model           string
	ClaudeSessionID string // opcional (pode ser preenchido depois)
	ResumedFrom     int64  // 0 quando não é retomada
}

// CreateSession registra uma nova sessão com status `running`.
func (s *Store) CreateSession(ctx context.Context, in SessionInput) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (issue_id, phase, claude_session_id, model, status, resumed_from)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		in.IssueID, string(in.Phase), nullStr(in.ClaudeSessionID), in.Model,
		string(domain.SessionRunning), nullInt64(in.ResumedFrom))
	if err != nil {
		return 0, fmt.Errorf("store: create session: %w", err)
	}
	return res.LastInsertId()
}

// SetClaudeSessionID correlaciona a sessão com o session_id do stream-json.
func (s *Store) SetClaudeSessionID(ctx context.Context, sessionID int64, claudeID string) error {
	return s.execAffect(ctx,
		`UPDATE sessions SET claude_session_id = ? WHERE id = ?`,
		nullStr(claudeID), sessionID)
}

// SetSessionContext registra o último uso de janela observado (0..1).
func (s *Store) SetSessionContext(ctx context.Context, sessionID int64, pct float64) error {
	return s.execAffect(ctx,
		`UPDATE sessions SET context_pct = ? WHERE id = ?`, pct, sessionID)
}

// EndSession encerra a sessão com o status final e o uso de contexto final.
func (s *Store) EndSession(ctx context.Context, sessionID int64, status domain.SessionStatus, contextPct float64) error {
	return s.execAffect(ctx,
		`UPDATE sessions SET status = ?, context_pct = ?, ended_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		string(status), contextPct, sessionID)
}

// GetSession lê uma sessão pelo id. ErrNotFound se ausente.
func (s *Store) GetSession(ctx context.Context, id int64) (domain.Session, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, issue_id, phase, claude_session_id, model, status,
		        context_pct, resumed_from, started_at, ended_at
		 FROM sessions WHERE id = ?`, id)
	return scanSession(row)
}

// ============================= approvals =============================

// OpenApproval cria uma aprovação `pending` e move a issue para
// `awaiting_approval`, numa transação. Retorna o id da aprovação.
func (s *Store) OpenApproval(ctx context.Context, issueID int64) (int64, error) {
	var approvalID int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO approvals (issue_id, state) VALUES (?, ?)`,
			issueID, string(domain.ApprovalPending))
		if err != nil {
			return err
		}
		approvalID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE issues SET phase = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			string(domain.PhaseAwaitingApproval), issueID); err != nil {
			return err
		}
		return auditTx(ctx, tx, issueID, "", "approval_opened", "{}")
	})
	if err != nil {
		return 0, fmt.Errorf("store: open approval %d: %w", issueID, err)
	}
	return approvalID, nil
}

// DecideApproval resolve a aprovação pendente da issue (approved/rejected),
// gravando motivo e chat_id. NÃO altera a fase da issue — a transição de label
// é responsabilidade do Scheduler (ver design.md §3). ErrNotFound se não há
// aprovação pendente.
func (s *Store) DecideApproval(ctx context.Context, issueID int64, approved bool, reason string, chatID int64) error {
	state := domain.ApprovalRejected
	if approved {
		state = domain.ApprovalApproved
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE approvals
			 SET state = ?, reason = ?, decided_at = CURRENT_TIMESTAMP, decided_by = ?
			 WHERE issue_id = ? AND state = ?`,
			string(state), nullStr(reason), chatID, issueID, string(domain.ApprovalPending))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return auditTx(ctx, tx, issueID, "", "approval_decided",
			fmt.Sprintf(`{"state":%q,"chat_id":%d}`, state, chatID))
	})
}

// GetPendingApproval retorna a aprovação pendente da issue. ErrNotFound se não
// houver. Útil para validar /approve e /reject (estado awaiting_approval).
func (s *Store) GetPendingApproval(ctx context.Context, issueID int64) (domain.Approval, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, issue_id, state, reason, requested_at, decided_at, decided_by
		 FROM approvals
		 WHERE issue_id = ? AND state = ?
		 ORDER BY id DESC LIMIT 1`,
		issueID, string(domain.ApprovalPending))
	return scanApproval(row)
}

// LastRejectionReason retorna o motivo do último /reject para a issue, ou ""
// se nunca foi rejeitada. Usado pelo runner para semear context.md.
func (s *Store) LastRejectionReason(ctx context.Context, issueID int64) (string, error) {
	var reason sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT reason FROM approvals
		 WHERE issue_id = ? AND state = ?
		 ORDER BY id DESC LIMIT 1`,
		issueID, string(domain.ApprovalRejected),
	).Scan(&reason)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: last rejection reason %d: %w", issueID, err)
	}
	return reason.String, nil
}

// ========================= processed_events ==========================

// MarkEventProcessed registra um evento externo como processado (idempotência).
// Idempotente: repetir o mesmo (source, id) não é erro.
func (s *Store) MarkEventProcessed(ctx context.Context, source, externalID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO processed_events (source, external_id) VALUES (?, ?)
		 ON CONFLICT(source, external_id) DO NOTHING`,
		source, externalID)
	if err != nil {
		return fmt.Errorf("store: mark event %s/%s: %w", source, externalID, err)
	}
	return nil
}

// WasProcessed informa se um evento externo já foi processado.
func (s *Store) WasProcessed(ctx context.Context, source, externalID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM processed_events WHERE source = ? AND external_id = ?`,
		source, externalID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: was processed %s/%s: %w", source, externalID, err)
	}
	return n > 0, nil
}

// ============================= audit_log =============================

// Audit registra uma entrada de auditoria avulsa. issueID 0 → sem issue.
func (s *Store) Audit(ctx context.Context, issueID int64, repo, event, detail string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_log (issue_id, repo, event, detail) VALUES (?, ?, ?, ?)`,
		nullInt64(issueID), nullStr(repo), event, nullStr(detail))
	if err != nil {
		return fmt.Errorf("store: audit: %w", err)
	}
	return nil
}

// ============================== /status ==============================

// StatusSnapshot agrega o estado para o comando /status: repos (+pausa),
// contagem de issues por fase e nº de sessões ativas.
func (s *Store) StatusSnapshot(ctx context.Context) (domain.StatusSnapshot, error) {
	var snap domain.StatusSnapshot
	snap.PhaseCounts = map[domain.Phase]int{}

	rows, err := s.db.QueryContext(ctx,
		`SELECT repo, paused, paused_reason, last_polled_at, etag, updated_at
		 FROM repo_state ORDER BY repo`)
	if err != nil {
		return snap, fmt.Errorf("store: status repos: %w", err)
	}
	for rows.Next() {
		rs, err := scanRepoState(rows)
		if err != nil {
			rows.Close()
			return snap, err
		}
		snap.Repos = append(snap.Repos, rs)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return snap, err
	}
	rows.Close()

	prows, err := s.db.QueryContext(ctx,
		`SELECT phase, COUNT(1) FROM issues GROUP BY phase`)
	if err != nil {
		return snap, fmt.Errorf("store: status phases: %w", err)
	}
	for prows.Next() {
		var phase string
		var n int
		if err := prows.Scan(&phase, &n); err != nil {
			prows.Close()
			return snap, err
		}
		snap.PhaseCounts[domain.Phase(phase)] = n
	}
	if err := prows.Err(); err != nil {
		prows.Close()
		return snap, err
	}
	prows.Close()

	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM sessions WHERE status = ?`, string(domain.SessionRunning),
	).Scan(&snap.ActiveSessions); err != nil {
		return snap, fmt.Errorf("store: status sessions: %w", err)
	}
	return snap, nil
}

// ============================== helpers ==============================

// tx executa fn dentro de uma transação, com rollback em caso de erro.
func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// execAffect roda um UPDATE/DELETE e retorna ErrNotFound se nada foi afetado.
func (s *Store) execAffect(ctx context.Context, query string, args ...any) error {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("store: exec: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// auditTx grava uma entrada de auditoria dentro de uma transação existente.
func auditTx(ctx context.Context, tx *sql.Tx, issueID int64, repo, event, detail string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO audit_log (issue_id, repo, event, detail) VALUES (?, ?, ?, ?)`,
		nullInt64(issueID), nullStr(repo), event, nullStr(detail))
	return err
}

// rowScanner abstrai *sql.Row e *sql.Rows para reuso dos scanners.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanIssue(r rowScanner) (domain.Issue, error) {
	var (
		i                                  domain.Issue
		title, specDir, ghURL, prURL, lerr sql.NullString
		phase                              string
		created, updated                   sql.NullString
	)
	err := r.Scan(&i.ID, &i.Repo, &i.Number, &title, &phase, &specDir,
		&ghURL, &prURL, &lerr, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Issue{}, ErrNotFound
	}
	if err != nil {
		return domain.Issue{}, err
	}
	i.Title = title.String
	i.Phase = domain.Phase(phase)
	i.SpecDir = specDir.String
	i.GitHubURL = ghURL.String
	i.PRURL = prURL.String
	i.LastError = lerr.String
	i.CreatedAt = parseTS(created)
	i.UpdatedAt = parseTS(updated)
	return i, nil
}

func scanSession(r rowScanner) (domain.Session, error) {
	var (
		s              domain.Session
		claudeID       sql.NullString
		status         string
		phase          string
		pct            sql.NullFloat64
		resumedFrom    sql.NullInt64
		started, ended sql.NullString
	)
	err := r.Scan(&s.ID, &s.IssueID, &phase, &claudeID, &s.Model, &status,
		&pct, &resumedFrom, &started, &ended)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Session{}, ErrNotFound
	}
	if err != nil {
		return domain.Session{}, err
	}
	s.Phase = domain.Phase(phase)
	s.ClaudeSessionID = claudeID.String
	s.Status = domain.SessionStatus(status)
	s.ContextPct = pct.Float64
	s.ResumedFrom = resumedFrom.Int64
	s.StartedAt = parseTS(started)
	s.EndedAt = parseTS(ended)
	return s, nil
}

func scanRepoState(r rowScanner) (domain.RepoState, error) {
	var (
		rs              domain.RepoState
		paused          int
		reason, etag    sql.NullString
		polled, updated sql.NullString
	)
	err := r.Scan(&rs.Repo, &paused, &reason, &polled, &etag, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RepoState{}, ErrNotFound
	}
	if err != nil {
		return domain.RepoState{}, err
	}
	rs.Paused = paused != 0
	rs.PausedReason = reason.String
	rs.ETag = etag.String
	rs.LastPolledAt = parseTS(polled)
	rs.UpdatedAt = parseTS(updated)
	return rs, nil
}

func scanApproval(r rowScanner) (domain.Approval, error) {
	var (
		a         domain.Approval
		state     string
		reason    sql.NullString
		requested sql.NullString
		decided   sql.NullString
		decidedBy sql.NullInt64
	)
	err := r.Scan(&a.ID, &a.IssueID, &state, &reason, &requested, &decided, &decidedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Approval{}, ErrNotFound
	}
	if err != nil {
		return domain.Approval{}, err
	}
	a.State = domain.ApprovalState(state)
	a.Reason = reason.String
	a.RequestedAt = parseTS(requested)
	a.DecidedAt = parseTS(decided)
	a.DecidedBy = decidedBy.Int64
	return a, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// ============================= previews ==============================

// CreatePreview insere um novo preview com status 'starting' e retorna o id.
func (s *Store) CreatePreview(ctx context.Context, issueID int64, repo string, port, extraPort int) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO previews (issue_id, repo, port, extra_port, status)
		 VALUES (?, ?, ?, ?, ?)`,
		issueID, repo, port, nullInt(extraPort), string(domain.PreviewStarting))
	if err != nil {
		return 0, fmt.Errorf("store: create preview issue=%d: %w", issueID, err)
	}
	return res.LastInsertId()
}

// SetPreviewRunning marca o preview como 'running' e grava a URL do túnel.
func (s *Store) SetPreviewRunning(ctx context.Context, id int64, tunnelURL string) error {
	return s.execAffect(ctx,
		`UPDATE previews SET status = ?, tunnel_url = ? WHERE id = ?`,
		string(domain.PreviewRunning), tunnelURL, id)
}

// StopPreview encerra um preview, gravando o status final e o motivo.
func (s *Store) StopPreview(ctx context.Context, id int64, status domain.PreviewStatus, reason domain.PreviewStopReason) error {
	return s.execAffect(ctx,
		`UPDATE previews SET status = ?, stop_reason = ?, stopped_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		string(status), string(reason), id)
}

// GetActivePreview retorna o preview ativo (starting|running|stopping), se
// houver. Retorna ErrNotFound quando não há nenhum ativo.
func (s *Store) GetActivePreview(ctx context.Context) (domain.Preview, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, issue_id, repo, port, extra_port, tunnel_url, status,
		        started_at, stopped_at, stop_reason
		 FROM previews
		 WHERE status IN ('starting','running','stopping')
		 ORDER BY id DESC LIMIT 1`)
	return scanPreview(row)
}

// GetPreviewByIssue retorna o preview mais recente de uma issue (qualquer
// status). Retorna ErrNotFound se a issue nunca teve preview.
func (s *Store) GetPreviewByIssue(ctx context.Context, issueID int64) (domain.Preview, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, issue_id, repo, port, extra_port, tunnel_url, status,
		        started_at, stopped_at, stop_reason
		 FROM previews WHERE issue_id = ? ORDER BY id DESC LIMIT 1`,
		issueID)
	return scanPreview(row)
}

// MarkStalePreviewsDead marca como 'dead' todos os previews que ficaram em
// estado transitório (starting|running|stopping) — chamado no startup para
// recuperação após restart do Argos. Retorna os ids marcados.
func (s *Store) MarkStalePreviewsDead(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`UPDATE previews
		 SET status = ?, stop_reason = ?, stopped_at = CURRENT_TIMESTAMP
		 WHERE status IN ('starting','running','stopping')
		 RETURNING id`,
		string(domain.PreviewDead), string(domain.PreviewStopRestart))
	if err != nil {
		return nil, fmt.Errorf("store: mark stale previews dead: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return ids, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListPreviews retorna previews filtrados por status. Se statusFilter estiver
// vazio, retorna todos. Ordenados do mais recente para o mais antigo.
func (s *Store) ListPreviews(ctx context.Context, statusFilter []domain.PreviewStatus) ([]domain.Preview, error) {
	var (
		query string
		args  []any
	)
	if len(statusFilter) == 0 {
		query = `SELECT id, issue_id, repo, port, extra_port, tunnel_url, status,
		                started_at, stopped_at, stop_reason
		         FROM previews ORDER BY id DESC`
	} else {
		placeholders := make([]string, len(statusFilter))
		for i, st := range statusFilter {
			placeholders[i] = "?"
			args = append(args, string(st))
		}
		query = `SELECT id, issue_id, repo, port, extra_port, tunnel_url, status,
		                started_at, stopped_at, stop_reason
		         FROM previews WHERE status IN (` + strings.Join(placeholders, ",") + `)
		         ORDER BY id DESC`
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list previews: %w", err)
	}
	defer rows.Close()
	var out []domain.Preview
	for rows.Next() {
		p, err := scanPreview(rows)
		if err != nil {
			return out, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func scanPreview(r rowScanner) (domain.Preview, error) {
	var (
		p                             domain.Preview
		extraPort                     sql.NullInt64
		tunnelURL, stopReason         sql.NullString
		status                        string
		startedAt, stoppedAt          sql.NullString
	)
	err := r.Scan(&p.ID, &p.IssueID, &p.Repo, &p.Port, &extraPort,
		&tunnelURL, &status, &startedAt, &stoppedAt, &stopReason)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Preview{}, ErrNotFound
	}
	if err != nil {
		return domain.Preview{}, err
	}
	p.ExtraPort = int(extraPort.Int64)
	p.TunnelURL = tunnelURL.String
	p.Status = domain.PreviewStatus(status)
	p.StopReason = domain.PreviewStopReason(stopReason.String)
	p.StartedAt = parseTS(startedAt)
	p.StoppedAt = parseTS(stoppedAt)
	return p, nil
}

func nullInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}
