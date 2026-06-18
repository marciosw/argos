package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/marciomacedo/argos/internal/domain"
)

// newTestStore abre um Store em memória, fechado ao fim do teste.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), Options{Path: ":memory:"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrationsApply(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// schema_migrations deve conter a versão 2 (0001_init + 0002_previews).
	var version int
	if err := s.db.QueryRowContext(ctx,
		`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("schema_migrations: %v", err)
	}
	if version != 2 {
		t.Fatalf("versão aplicada = %d, quero 2", version)
	}

	// Reabrir/migrar de novo deve ser idempotente (sem erro, sem duplicar).
	if err := s.migrate(ctx); err != nil {
		t.Fatalf("migrate idempotente: %v", err)
	}
}

func TestOpenFileBacked(t *testing.T) {
	// Confirma que o banco inicializa em arquivo (modo WAL) sem CGO.
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "orchestrator.db")
	s, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("Open file: %v", err)
	}
	defer s.Close()

	var mode string
	if err := s.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, quero wal", mode)
	}

	id, err := s.UpsertIssue(ctx, domain.RepoWeb, 1, "t", "", domain.PhaseReady)
	if err != nil || id == 0 {
		t.Fatalf("UpsertIssue file: id=%d err=%v", id, err)
	}
}

func TestUpsertAndGetIssue(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	id, err := s.UpsertIssue(ctx, domain.RepoWeb, 42, "Título", "https://gh/42", domain.PhaseReady)
	if err != nil {
		t.Fatalf("UpsertIssue: %v", err)
	}
	if id == 0 {
		t.Fatal("id zero")
	}

	got, err := s.GetIssue(ctx, domain.RepoWeb, 42)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.ID != id || got.Title != "Título" || got.Phase != domain.PhaseReady {
		t.Fatalf("issue inesperada: %+v", got)
	}
	if got.GitHubURL != "https://gh/42" {
		t.Fatalf("github_url = %q", got.GitHubURL)
	}

	// Upsert idempotente: mesmo (repo, number) → mesmo id, título atualizado,
	// fase preservada.
	id2, err := s.UpsertIssue(ctx, domain.RepoWeb, 42, "Novo título", "https://gh/42", domain.PhaseDoing)
	if err != nil {
		t.Fatalf("UpsertIssue 2: %v", err)
	}
	if id2 != id {
		t.Fatalf("id mudou no upsert: %d != %d", id2, id)
	}
	got2, _ := s.GetIssue(ctx, domain.RepoWeb, 42)
	if got2.Title != "Novo título" {
		t.Fatalf("título não atualizado: %q", got2.Title)
	}
	if got2.Phase != domain.PhaseReady {
		t.Fatalf("fase deveria ser preservada, veio %q", got2.Phase)
	}
}

func TestGetIssueNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetIssue(context.Background(), domain.RepoMobile, 999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("quero ErrNotFound, veio %v", err)
	}
}

func TestSetPhaseAndFields(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	id, _ := s.UpsertIssue(ctx, domain.RepoWeb, 1, "t", "", domain.PhaseReady)

	if err := s.SetPhase(ctx, id, domain.PhaseDoing); err != nil {
		t.Fatalf("SetPhase: %v", err)
	}
	if err := s.SetSpecDir(ctx, id, "docs/specs/issue-1/"); err != nil {
		t.Fatalf("SetSpecDir: %v", err)
	}
	if err := s.SetPRURL(ctx, id, "https://gh/pr/1"); err != nil {
		t.Fatalf("SetPRURL: %v", err)
	}
	if err := s.SetLastError(ctx, id, "boom"); err != nil {
		t.Fatalf("SetLastError: %v", err)
	}

	got, _ := s.GetIssueByID(ctx, id)
	if got.Phase != domain.PhaseDoing || got.SpecDir != "docs/specs/issue-1/" ||
		got.PRURL != "https://gh/pr/1" || got.LastError != "boom" {
		t.Fatalf("campos inesperados: %+v", got)
	}

	// SetPhase em issue inexistente → ErrNotFound.
	if err := s.SetPhase(ctx, 99999, domain.PhaseDone); !errors.Is(err, ErrNotFound) {
		t.Fatalf("quero ErrNotFound, veio %v", err)
	}

	// phase_change deve ter sido auditado.
	var n int
	s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM audit_log WHERE issue_id = ? AND event = 'phase_change'`, id).Scan(&n)
	if n == 0 {
		t.Fatal("phase_change não auditado")
	}
}

func TestLockLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	id, _ := s.UpsertIssue(ctx, domain.RepoWeb, 7, "t", "", domain.PhaseTodo)

	// Adquire com sucesso.
	ok, err := s.AcquireLock(ctx, id, "worker-a", time.Minute)
	if err != nil || !ok {
		t.Fatalf("AcquireLock 1: ok=%v err=%v", ok, err)
	}

	// Segunda tentativa (lock vivo) falha sem erro.
	ok, err = s.AcquireLock(ctx, id, "worker-b", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLock 2 erro: %v", err)
	}
	if ok {
		t.Fatal("AcquireLock 2 deveria falhar (lock vivo)")
	}

	// Após liberar, adquire de novo.
	if err := s.ReleaseLock(ctx, id); err != nil {
		t.Fatalf("ReleaseLock: %v", err)
	}
	ok, _ = s.AcquireLock(ctx, id, "worker-c", time.Minute)
	if !ok {
		t.Fatal("AcquireLock 3 deveria suceder após release")
	}
}

func TestLockExpiryAndReap(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	id, _ := s.UpsertIssue(ctx, domain.RepoWeb, 8, "t", "", domain.PhaseTodo)

	// Lock já expirado (lease negativo).
	ok, err := s.AcquireLock(ctx, id, "worker-a", -time.Minute)
	if err != nil || !ok {
		t.Fatalf("AcquireLock expirado: ok=%v err=%v", ok, err)
	}

	// Outro worker consegue adquirir por cima de um lock expirado.
	ok, err = s.AcquireLock(ctx, id, "worker-b", time.Minute)
	if err != nil || !ok {
		t.Fatalf("AcquireLock sobre expirado: ok=%v err=%v", ok, err)
	}

	// Reap remove o que estiver expirado. Novo lock expirado e depois reap.
	s.ReleaseLock(ctx, id)
	s.AcquireLock(ctx, id, "worker-c", -time.Minute)
	reaped, err := s.ReapExpiredLocks(ctx, time.Now())
	if err != nil {
		t.Fatalf("ReapExpiredLocks: %v", err)
	}
	if reaped != 1 {
		t.Fatalf("reaped = %d, quero 1", reaped)
	}
}

func TestSessionLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	id, _ := s.UpsertIssue(ctx, domain.RepoMobile, 3, "t", "", domain.PhaseDoing)

	sid, err := s.CreateSession(ctx, SessionInput{
		IssueID: id, Phase: domain.PhaseDoing, Model: "claude-opus-4-5",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := s.SetClaudeSessionID(ctx, sid, "sess-abc"); err != nil {
		t.Fatalf("SetClaudeSessionID: %v", err)
	}
	if err := s.SetSessionContext(ctx, sid, 0.42); err != nil {
		t.Fatalf("SetSessionContext: %v", err)
	}

	got, err := s.GetSession(ctx, sid)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Status != domain.SessionRunning || got.ClaudeSessionID != "sess-abc" || got.ContextPct != 0.42 {
		t.Fatalf("sessão inesperada: %+v", got)
	}

	// Sessão ativa contabilizada no snapshot.
	snap, _ := s.StatusSnapshot(ctx)
	if snap.ActiveSessions != 1 {
		t.Fatalf("ActiveSessions = %d, quero 1", snap.ActiveSessions)
	}

	if err := s.EndSession(ctx, sid, domain.SessionCompleted, 0.55); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	got, _ = s.GetSession(ctx, sid)
	if got.Status != domain.SessionCompleted || got.ContextPct != 0.55 || got.EndedAt.IsZero() {
		t.Fatalf("sessão pós-encerramento inesperada: %+v", got)
	}

	// Retomada referenciando a sessão anterior.
	sid2, err := s.CreateSession(ctx, SessionInput{
		IssueID: id, Phase: domain.PhaseDoing, Model: "claude-opus-4-5", ResumedFrom: sid,
	})
	if err != nil {
		t.Fatalf("CreateSession resumed: %v", err)
	}
	got2, _ := s.GetSession(ctx, sid2)
	if got2.ResumedFrom != sid {
		t.Fatalf("ResumedFrom = %d, quero %d", got2.ResumedFrom, sid)
	}
}

func TestApprovalFlow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	id, _ := s.UpsertIssue(ctx, domain.RepoWeb, 10, "t", "", domain.PhaseDocumentation)

	if _, err := s.OpenApproval(ctx, id); err != nil {
		t.Fatalf("OpenApproval: %v", err)
	}

	// Issue foi para awaiting_approval.
	iss, _ := s.GetIssueByID(ctx, id)
	if iss.Phase != domain.PhaseAwaitingApproval {
		t.Fatalf("fase = %q, quero awaiting_approval", iss.Phase)
	}

	// Aprovação pendente existe.
	ap, err := s.GetPendingApproval(ctx, id)
	if err != nil {
		t.Fatalf("GetPendingApproval: %v", err)
	}
	if ap.State != domain.ApprovalPending {
		t.Fatalf("state = %q", ap.State)
	}

	// Decidir (rejeitar) com motivo.
	if err := s.DecideApproval(ctx, id, false, "faltou X", 555); err != nil {
		t.Fatalf("DecideApproval: %v", err)
	}

	// Não há mais pendente.
	if _, err := s.GetPendingApproval(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("quero ErrNotFound após decidir, veio %v", err)
	}

	// Decidir de novo (sem pendente) → ErrNotFound.
	if err := s.DecideApproval(ctx, id, true, "", 555); !errors.Is(err, ErrNotFound) {
		t.Fatalf("quero ErrNotFound, veio %v", err)
	}

	// O registro decidido guardou estado/motivo/chat.
	var state, reason string
	var by int64
	s.db.QueryRowContext(ctx,
		`SELECT state, reason, decided_by FROM approvals WHERE issue_id = ?`, id).
		Scan(&state, &reason, &by)
	if state != string(domain.ApprovalRejected) || reason != "faltou X" || by != 555 {
		t.Fatalf("approval decidida inesperada: state=%s reason=%s by=%d", state, reason, by)
	}
}

func TestRepoStateAndActiveRepos(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for _, r := range domain.Repos {
		if err := s.EnsureRepo(ctx, r); err != nil {
			t.Fatalf("EnsureRepo %s: %v", r, err)
		}
	}
	// EnsureRepo idempotente.
	if err := s.EnsureRepo(ctx, domain.RepoWeb); err != nil {
		t.Fatalf("EnsureRepo idempotente: %v", err)
	}

	active, _ := s.ListActiveRepos(ctx)
	if len(active) != 3 {
		t.Fatalf("ativos = %v, quero 3", active)
	}

	// Pausar mobile.
	if err := s.SetRepoPaused(ctx, domain.RepoMobile, true, "manutenção"); err != nil {
		t.Fatalf("SetRepoPaused: %v", err)
	}
	active, _ = s.ListActiveRepos(ctx)
	if len(active) != 2 {
		t.Fatalf("ativos pós-pausa = %v, quero 2", active)
	}
	rs, _ := s.GetRepoState(ctx, domain.RepoMobile)
	if !rs.Paused || rs.PausedReason != "manutenção" {
		t.Fatalf("repo_state inesperado: %+v", rs)
	}

	// Retomar.
	s.SetRepoPaused(ctx, domain.RepoMobile, false, "")
	active, _ = s.ListActiveRepos(ctx)
	if len(active) != 3 {
		t.Fatalf("ativos pós-resume = %v, quero 3", active)
	}

	// Cursor de polling.
	now := time.Now()
	if err := s.UpdatePollCursor(ctx, domain.RepoWeb, now, `W/"etag"`); err != nil {
		t.Fatalf("UpdatePollCursor: %v", err)
	}
	rs, _ = s.GetRepoState(ctx, domain.RepoWeb)
	if rs.ETag != `W/"etag"` || rs.LastPolledAt.IsZero() {
		t.Fatalf("cursor não persistido: %+v", rs)
	}
}

func TestProcessedEvents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	ok, _ := s.WasProcessed(ctx, "telegram", "100")
	if ok {
		t.Fatal("não deveria estar processado ainda")
	}
	if err := s.MarkEventProcessed(ctx, "telegram", "100"); err != nil {
		t.Fatalf("MarkEventProcessed: %v", err)
	}
	// Repetir é idempotente (sem erro).
	if err := s.MarkEventProcessed(ctx, "telegram", "100"); err != nil {
		t.Fatalf("MarkEventProcessed repetido: %v", err)
	}
	ok, _ = s.WasProcessed(ctx, "telegram", "100")
	if !ok {
		t.Fatal("deveria estar processado")
	}
	// Outra fonte com mesmo id é independente.
	ok, _ = s.WasProcessed(ctx, "github", "100")
	if ok {
		t.Fatal("fonte github não deveria estar processada")
	}
}

func TestStatusSnapshot(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, r := range domain.Repos {
		s.EnsureRepo(ctx, r)
	}
	s.UpsertIssue(ctx, domain.RepoWeb, 1, "a", "", domain.PhaseDoing)
	s.UpsertIssue(ctx, domain.RepoWeb, 2, "b", "", domain.PhaseDoing)
	s.UpsertIssue(ctx, domain.RepoMobile, 3, "c", "", domain.PhaseTodo)

	snap, err := s.StatusSnapshot(ctx)
	if err != nil {
		t.Fatalf("StatusSnapshot: %v", err)
	}
	if len(snap.Repos) != 3 {
		t.Fatalf("repos = %d, quero 3", len(snap.Repos))
	}
	if snap.PhaseCounts[domain.PhaseDoing] != 2 || snap.PhaseCounts[domain.PhaseTodo] != 1 {
		t.Fatalf("phase counts inesperados: %v", snap.PhaseCounts)
	}
}

func TestAudit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	id, _ := s.UpsertIssue(ctx, domain.RepoWeb, 1, "t", "", domain.PhaseReady)

	if err := s.Audit(ctx, id, domain.RepoWeb, "command", `{"cmd":"run"}`); err != nil {
		t.Fatalf("Audit: %v", err)
	}
	// Auditoria sem issue (issueID 0).
	if err := s.Audit(ctx, 0, "", "startup", ""); err != nil {
		t.Fatalf("Audit sem issue: %v", err)
	}

	var n int
	s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM audit_log`).Scan(&n)
	if n < 2 {
		t.Fatalf("audit_log tem %d linhas, quero >=2", n)
	}
}

func TestPreviewLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	issueID, _ := s.UpsertIssue(ctx, domain.RepoWeb, 5, "preview test", "", domain.PhaseDoing)

	// Nenhum preview ativo inicialmente.
	if _, err := s.GetActivePreview(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("quero ErrNotFound sem preview ativo, veio %v", err)
	}

	// Criar preview (starting).
	pid, err := s.CreatePreview(ctx, issueID, domain.RepoWeb, 9000, 9001)
	if err != nil || pid == 0 {
		t.Fatalf("CreatePreview: id=%d err=%v", pid, err)
	}

	// Deve aparecer como ativo.
	active, err := s.GetActivePreview(ctx)
	if err != nil {
		t.Fatalf("GetActivePreview: %v", err)
	}
	if active.ID != pid || active.Status != domain.PreviewStarting {
		t.Fatalf("ativo inesperado: %+v", active)
	}
	if active.Port != 9000 || active.ExtraPort != 9001 {
		t.Fatalf("portas: port=%d extra=%d", active.Port, active.ExtraPort)
	}

	// Marcar como running com URL.
	if err := s.SetPreviewRunning(ctx, pid, "https://abc.trycloudflare.com"); err != nil {
		t.Fatalf("SetPreviewRunning: %v", err)
	}
	active, _ = s.GetActivePreview(ctx)
	if active.Status != domain.PreviewRunning || active.TunnelURL != "https://abc.trycloudflare.com" {
		t.Fatalf("após running: %+v", active)
	}

	// GetPreviewByIssue encontra o preview.
	byIssue, err := s.GetPreviewByIssue(ctx, issueID)
	if err != nil {
		t.Fatalf("GetPreviewByIssue: %v", err)
	}
	if byIssue.ID != pid {
		t.Fatalf("id por issue = %d, quero %d", byIssue.ID, pid)
	}

	// Parar por comando.
	if err := s.StopPreview(ctx, pid, domain.PreviewStopped, domain.PreviewStopCommand); err != nil {
		t.Fatalf("StopPreview: %v", err)
	}

	// Não está mais ativo.
	if _, err := s.GetActivePreview(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("quero ErrNotFound após stop, veio %v", err)
	}

	// GetPreviewByIssue ainda encontra (status stopped).
	byIssue, _ = s.GetPreviewByIssue(ctx, issueID)
	if byIssue.Status != domain.PreviewStopped || byIssue.StopReason != domain.PreviewStopCommand {
		t.Fatalf("preview parado inesperado: %+v", byIssue)
	}
	if byIssue.StoppedAt.IsZero() {
		t.Fatal("stopped_at não preenchido")
	}
}

func TestPreviewMarkStaleAndList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	id1, _ := s.UpsertIssue(ctx, domain.RepoWeb, 11, "a", "", domain.PhaseDoing)
	id2, _ := s.UpsertIssue(ctx, domain.RepoMobile, 12, "b", "", domain.PhaseDoing)

	// Dois previews em estados transitórios simulando crash do Argos.
	pid1, _ := s.CreatePreview(ctx, id1, domain.RepoWeb, 9000, 0)
	pid2, _ := s.CreatePreview(ctx, id2, domain.RepoMobile, 9010, 0)
	s.SetPreviewRunning(ctx, pid2, "https://xyz.trycloudflare.com")

	// MarkStalePreviewsDead deve marcar os dois.
	dead, err := s.MarkStalePreviewsDead(ctx)
	if err != nil {
		t.Fatalf("MarkStalePreviewsDead: %v", err)
	}
	if len(dead) != 2 {
		t.Fatalf("mortos = %d, quero 2", len(dead))
	}

	// Após recovery, nenhum ativo.
	if _, err := s.GetActivePreview(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("quero ErrNotFound após recovery, veio %v", err)
	}

	// Chamada idempotente (zero previews transitórios restantes).
	dead2, err := s.MarkStalePreviewsDead(ctx)
	if err != nil {
		t.Fatalf("MarkStalePreviewsDead idempotente: %v", err)
	}
	if len(dead2) != 0 {
		t.Fatalf("segunda passada marcou %d, quero 0", len(dead2))
	}

	// ListPreviews retorna todos (sem filtro).
	all, err := s.ListPreviews(ctx, nil)
	if err != nil {
		t.Fatalf("ListPreviews: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("listados = %d, quero 2", len(all))
	}

	// ListPreviews com filtro de status.
	deadList, err := s.ListPreviews(ctx, []domain.PreviewStatus{domain.PreviewDead})
	if err != nil {
		t.Fatalf("ListPreviews dead: %v", err)
	}
	if len(deadList) != 2 {
		t.Fatalf("dead listados = %d, quero 2", len(deadList))
	}

	// Filtro que não bate com nada.
	running, _ := s.ListPreviews(ctx, []domain.PreviewStatus{domain.PreviewRunning})
	if len(running) != 0 {
		t.Fatalf("running = %d, quero 0", len(running))
	}

	// Verifica stop_reason = restart e ids presentes.
	idSet := map[int64]bool{pid1: false, pid2: false}
	for _, p := range deadList {
		if p.StopReason != domain.PreviewStopRestart {
			t.Fatalf("stop_reason = %q, quero restart", p.StopReason)
		}
		idSet[p.ID] = true
	}
	for pid, seen := range idSet {
		if !seen {
			t.Fatalf("preview %d não encontrado na lista", pid)
		}
	}
}
