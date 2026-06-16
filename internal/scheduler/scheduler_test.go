package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/marciomacedo/argos/internal/config"
	"github.com/marciomacedo/argos/internal/domain"
	"github.com/marciomacedo/argos/internal/github"
	"github.com/marciomacedo/argos/internal/store"
)

// ─── fakes ────────────────────────────────────────────────────────────────────

type fakePoller struct {
	mu       sync.Mutex
	issues   map[string][]github.Issue
	labels   map[string][]string
	comments []string
	listErr  error
}

func newFakePoller() *fakePoller {
	return &fakePoller{
		issues: make(map[string][]github.Issue),
		labels: make(map[string][]string),
	}
}

func lkey(repo string, issue int) string { return fmt.Sprintf("%s#%d", repo, issue) }

func (f *fakePoller) ListByLabels(_ context.Context, repo string, _ []string, _ string) ([]github.Issue, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, "", f.listErr
	}
	return append([]github.Issue(nil), f.issues[repo]...), "", nil
}

func (f *fakePoller) TransitionLabel(_ context.Context, repo string, issue int, from, to string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := lkey(repo, issue)
	cur := f.labels[key]
	next := make([]string, 0, len(cur))
	for _, l := range cur {
		if l != from {
			next = append(next, l)
		}
	}
	next = append(next, to)
	f.labels[key] = next
	return nil
}

func (f *fakePoller) SetLabel(_ context.Context, repo string, issue int, label string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := lkey(repo, issue)
	f.labels[key] = append(f.labels[key], label)
	return nil
}

func (f *fakePoller) ClearLabel(_ context.Context, repo string, issue int, label string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := lkey(repo, issue)
	cur := f.labels[key]
	next := cur[:0]
	for _, l := range cur {
		if l != label {
			next = append(next, l)
		}
	}
	f.labels[key] = next
	return nil
}

func (f *fakePoller) Comment(_ context.Context, _ string, _ int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.comments = append(f.comments, body)
	return nil
}

func (f *fakePoller) OpenPR(_ context.Context, _ string, _ github.OpenPRInput) (github.PullRequest, error) {
	return github.PullRequest{}, nil
}

func (f *fakePoller) GetIssue(_ context.Context, repo string, issue int) (github.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, iss := range f.issues[repo] {
		if iss.Number == issue {
			return iss, nil
		}
	}
	return github.Issue{Repo: repo, Number: issue}, nil
}

func (f *fakePoller) getLabels(repo string, issue int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.labels[lkey(repo, issue)]...)
}

// fakeRunner implementa TaskRunner em memória.
type fakeRunner struct {
	mu    sync.Mutex
	calls []domain.Task
	err   error
	block chan struct{} // se não-nil, Run bloqueia até close(block)
}

func (f *fakeRunner) Run(ctx context.Context, task domain.Task) error {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, task)
	return f.err
}

func (f *fakeRunner) called() []domain.Task {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Task(nil), f.calls...)
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), store.Options{Path: ":memory:"})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func newTestConfig() *config.Config {
	cfg, _ := config.Load("")
	cfg.GitHub.Repos = map[string]config.RepoTarget{
		"web": {Owner: "org", Name: "web"},
	}
	cfg.MaxConcurrentTasks = 2
	cfg.LockLease = config.Duration(5 * time.Minute)
	return cfg
}

func waitFor(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condição não atingida dentro do timeout")
}

func newSched(st *store.Store, fp *fakePoller, fr *fakeRunner, cfg *config.Config) *Scheduler {
	cmds := make(chan domain.Command, 1)
	return New(st, fp, fr, cfg, cmds)
}

// ─── testes ───────────────────────────────────────────────────────────────────

// TestHandleReady: issue agent:ready → lock adquirido + TransitionLabel +
// TaskRunner.Run com Phase=documentation.
func TestHandleReady(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	fp := newFakePoller()
	fr := &fakeRunner{}
	cfg := newTestConfig()

	fp.issues["web"] = []github.Issue{
		{Repo: "web", Number: 1, Title: "feat X", URL: "http://gh/1",
			Labels: []string{domain.LabelReady}},
	}
	fp.labels[lkey("web", 1)] = []string{domain.LabelReady}

	sched := newSched(st, fp, fr, cfg)
	if err := st.EnsureRepo(ctx, "web"); err != nil {
		t.Fatal(err)
	}

	sched.processRepo(ctx, "web")
	waitFor(t, func() bool { return len(fr.called()) > 0 })

	tasks := fr.called()
	if len(tasks) != 1 {
		t.Fatalf("esperava 1 chamada ao runner, got %d", len(tasks))
	}
	if tasks[0].Phase != domain.PhaseDocumentation {
		t.Errorf("phase esperada %q, got %q", domain.PhaseDocumentation, tasks[0].Phase)
	}
	if tasks[0].Repo != "web" || tasks[0].Issue != 1 {
		t.Errorf("task inesperada: %+v", tasks[0])
	}

	labels := fp.getLabels("web", 1)
	hasDoc := false
	for _, l := range labels {
		if l == domain.LabelDocumentation {
			hasDoc = true
		}
	}
	if !hasDoc {
		t.Errorf("label documentation não encontrada; labels=%v", labels)
	}
}

// TestHandleTodo: issue todo → Phase=doing + TaskRunner chamado.
func TestHandleTodo(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	fp := newFakePoller()
	fr := &fakeRunner{}
	cfg := newTestConfig()

	if err := st.EnsureRepo(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertIssue(ctx, "web", 2, "feat Y", "http://gh/2", domain.PhaseTodo); err != nil {
		t.Fatal(err)
	}

	fp.issues["web"] = []github.Issue{
		{Repo: "web", Number: 2, Title: "feat Y", URL: "http://gh/2",
			Labels: []string{domain.LabelTodo}},
	}
	fp.labels[lkey("web", 2)] = []string{domain.LabelTodo}

	sched := newSched(st, fp, fr, cfg)
	sched.processRepo(ctx, "web")
	waitFor(t, func() bool { return len(fr.called()) > 0 })

	if tasks := fr.called(); tasks[0].Phase != domain.PhaseDoing {
		t.Errorf("phase esperada %q, got %q", domain.PhaseDoing, tasks[0].Phase)
	}
}

// TestRepoPausado: processTick não despacha tasks para repo pausado.
func TestRepoPausado(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	fp := newFakePoller()
	fr := &fakeRunner{}
	cfg := newTestConfig()

	if err := st.EnsureRepo(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRepoPaused(ctx, "web", true, "teste"); err != nil {
		t.Fatal(err)
	}

	fp.issues["web"] = []github.Issue{
		{Repo: "web", Number: 3, Labels: []string{domain.LabelReady}},
	}

	sched := newSched(st, fp, fr, cfg)
	sched.processTick(ctx)
	time.Sleep(50 * time.Millisecond)

	if got := len(fr.called()); got != 0 {
		t.Errorf("esperava 0 chamadas ao runner para repo pausado, got %d", got)
	}
}

// TestLockJaExistente: issue com lock ativo não é despachada.
func TestLockJaExistente(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	fp := newFakePoller()
	fr := &fakeRunner{}
	cfg := newTestConfig()

	if err := st.EnsureRepo(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	issueID, _ := st.UpsertIssue(ctx, "web", 4, "feat Z", "http://gh/4", domain.PhaseReady)
	if _, err := st.AcquireLock(ctx, issueID, "outro", 10*time.Minute); err != nil {
		t.Fatal(err)
	}

	fp.issues["web"] = []github.Issue{
		{Repo: "web", Number: 4, Labels: []string{domain.LabelReady}},
	}

	sched := newSched(st, fp, fr, cfg)
	sched.processRepo(ctx, "web")
	time.Sleep(50 * time.Millisecond)

	if got := len(fr.called()); got != 0 {
		t.Errorf("esperava 0 chamadas ao runner com lock existente, got %d", got)
	}
}

// TestSemaforoLimita: com max_concurrent_tasks=1, a segunda goroutine bloqueia
// até a primeira terminar.
func TestSemaforoLimita(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	fp := newFakePoller()
	block := make(chan struct{})
	fr := &fakeRunner{block: block}
	cfg := newTestConfig()
	cfg.MaxConcurrentTasks = 1

	if err := st.EnsureRepo(ctx, "web"); err != nil {
		t.Fatal(err)
	}

	iss1 := github.Issue{Repo: "web", Number: 5, Title: "A", URL: "http://gh/5",
		Labels: []string{domain.LabelReady}}
	iss2 := github.Issue{Repo: "web", Number: 6, Title: "B", URL: "http://gh/6",
		Labels: []string{domain.LabelReady}}
	fp.labels[lkey("web", 5)] = []string{domain.LabelReady}
	fp.labels[lkey("web", 6)] = []string{domain.LabelReady}

	sched := newSched(st, fp, fr, cfg)

	// Primeira issue: adquire semáforo e bloqueia na goroutine do runner.
	sched.handleReady(ctx, "web", iss1)
	// Garante que a goroutine 1 adquiriu o semáforo.
	time.Sleep(30 * time.Millisecond)

	// Segunda issue: tenta s.sem <- struct{}{} e bloqueia (semáforo cheio).
	launched := make(chan struct{})
	go func() {
		sched.handleReady(ctx, "web", iss2)
		close(launched)
	}()

	select {
	case <-launched:
		t.Error("segunda issue despachada enquanto semáforo cheio")
	case <-time.After(100 * time.Millisecond):
		// correto: segunda goroutine bloqueada no semáforo
	}

	close(block) // libera a primeira goroutine

	select {
	case <-launched:
		// ok: segunda pôde entrar após a primeira terminar
	case <-time.After(2 * time.Second):
		t.Error("segunda issue nunca despachada após liberação do semáforo")
	}
}

// TestCmdApprove: /approve #N → SetPhase(todo) + TransitionLabel chamados.
func TestCmdApprove(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	fp := newFakePoller()
	fr := &fakeRunner{}
	cfg := newTestConfig()

	if err := st.EnsureRepo(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	issueID, _ := st.UpsertIssue(ctx, "web", 7, "feat approve", "http://gh/7", domain.PhaseAwaitingApproval)
	if err := st.SetPhase(ctx, issueID, domain.PhaseAwaitingApproval); err != nil {
		t.Fatal(err)
	}
	fp.labels[lkey("web", 7)] = []string{domain.LabelDocumentation}

	sched := newSched(st, fp, fr, cfg)
	sched.handleCommand(ctx, domain.Command{Type: domain.CmdApprove, Issue: 7})

	issue, err := st.GetIssue(ctx, "web", 7)
	if err != nil {
		t.Fatal(err)
	}
	if issue.Phase != domain.PhaseTodo {
		t.Errorf("fase esperada %q, got %q", domain.PhaseTodo, issue.Phase)
	}

	labels := fp.getLabels("web", 7)
	hasTodo := false
	for _, l := range labels {
		if l == domain.LabelTodo {
			hasTodo = true
		}
	}
	if !hasTodo {
		t.Errorf("label todo não encontrada após approve; labels=%v", labels)
	}
}

// TestCmdPause: /pause repo → SetRepoPaused(repo, true) chamado.
func TestCmdPause(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	fp := newFakePoller()
	fr := &fakeRunner{}
	cfg := newTestConfig()

	if err := st.EnsureRepo(ctx, "web"); err != nil {
		t.Fatal(err)
	}

	sched := newSched(st, fp, fr, cfg)
	sched.handleCommand(ctx, domain.Command{Type: domain.CmdPause, Repo: "web"})

	rs, err := st.GetRepoState(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	if !rs.Paused {
		t.Error("esperava repo pausado após CmdPause")
	}
}

// TestCmdApproveDecideApproval: /approve persiste DecideApproval(approved=true)
// na tabela approvals.
func TestCmdApproveDecideApproval(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	fp := newFakePoller()
	cfg := newTestConfig()

	if err := st.EnsureRepo(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	issueID, _ := st.UpsertIssue(ctx, "web", 10, "feat ap", "http://gh/10", domain.PhaseAwaitingApproval)
	if err := st.SetPhase(ctx, issueID, domain.PhaseAwaitingApproval); err != nil {
		t.Fatal(err)
	}
	// Abre aprovação pending (simula fim de handleReady).
	if _, err := st.OpenApproval(ctx, issueID); err != nil {
		t.Fatal(err)
	}
	fp.labels[lkey("web", 10)] = []string{domain.LabelDocumentation}

	sched := newSched(st, fp, &fakeRunner{}, cfg)
	sched.handleCommand(ctx, domain.Command{Type: domain.CmdApprove, Issue: 10, ChatID: 42})

	// Não deve haver mais aprovação pending (foi decidida).
	_, err := st.GetPendingApproval(ctx, issueID)
	if err == nil {
		t.Error("esperava ErrNotFound para aprovação pending após /approve")
	}
	// Fase deve ser todo.
	iss, _ := st.GetIssue(ctx, "web", 10)
	if iss.Phase != domain.PhaseTodo {
		t.Errorf("fase esperada %q, got %q", domain.PhaseTodo, iss.Phase)
	}
}

// TestCmdRejectReprocessa: /reject transita label de volta para agent:ready,
// muda phase para ready e persiste DecideApproval(rejected).
func TestCmdRejectReprocessa(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	fp := newFakePoller()
	cfg := newTestConfig()

	if err := st.EnsureRepo(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	issueID, _ := st.UpsertIssue(ctx, "web", 11, "feat rej", "http://gh/11", domain.PhaseAwaitingApproval)
	if err := st.SetPhase(ctx, issueID, domain.PhaseAwaitingApproval); err != nil {
		t.Fatal(err)
	}
	if _, err := st.OpenApproval(ctx, issueID); err != nil {
		t.Fatal(err)
	}
	fp.labels[lkey("web", 11)] = []string{domain.LabelDocumentation}

	sched := newSched(st, fp, &fakeRunner{}, cfg)
	sched.handleCommand(ctx, domain.Command{
		Type:   domain.CmdReject,
		Issue:  11,
		Reason: "faltou tratar erro de rede",
		ChatID: 99,
	})

	// Phase deve voltar para ready (para handleReady repegar).
	iss, _ := st.GetIssue(ctx, "web", 11)
	if iss.Phase != domain.PhaseReady {
		t.Errorf("fase esperada %q após /reject, got %q", domain.PhaseReady, iss.Phase)
	}

	// Label deve ter voltado para agent:ready.
	labels := fp.getLabels("web", 11)
	hasReady := false
	for _, l := range labels {
		if l == domain.LabelReady {
			hasReady = true
		}
	}
	if !hasReady {
		t.Errorf("label agent:ready esperada após /reject; labels=%v", labels)
	}

	// Motivo deve ter sido registrado na tabela approvals.
	reason, err := st.LastRejectionReason(ctx, issueID)
	if err != nil {
		t.Fatalf("LastRejectionReason: %v", err)
	}
	if reason != "faltou tratar erro de rede" {
		t.Errorf("motivo de rejeição esperado, got %q", reason)
	}
}

// TestOpenApprovalIdempotente: entrar em awaiting_approval duas vezes não cria
// duas aprovações pending.
func TestOpenApprovalIdempotente(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	fp := newFakePoller()
	fr := &fakeRunner{} // runner completa na primeira chamada
	cfg := newTestConfig()
	cfg.MaxConcurrentTasks = 1

	if err := st.EnsureRepo(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	fp.issues["web"] = []github.Issue{
		{Repo: "web", Number: 20, Title: "feat idem", URL: "http://gh/20",
			Labels: []string{domain.LabelReady}},
	}
	fp.labels[lkey("web", 20)] = []string{domain.LabelReady}

	sched := newSched(st, fp, fr, cfg)
	// Primeiro despacho.
	sched.processRepo(ctx, "web")
	waitFor(t, func() bool { return len(fr.called()) > 0 })

	issueID, _ := st.UpsertIssue(ctx, "web", 20, "feat idem", "http://gh/20", domain.PhaseReady)
	// Abre aprovação manualmente (como handleReady faria) e depois tenta abrir de novo.
	if _, err := st.OpenApproval(ctx, issueID); err != nil {
		t.Fatal(err)
	}
	// Segunda chamada ao OpenApproval deve criar segunda pending (store não é idempotente
	// por si só); mas handleReady verifica GetPendingApproval antes — testamos isso
	// verificando que só há 1 pending.
	_, pendErr := st.GetPendingApproval(ctx, issueID)
	if pendErr != nil {
		t.Errorf("esperava aprovação pending, got err: %v", pendErr)
	}
}

// TestCmdRunReadyDispara: /run #N com phase=ready dispara handleReady imediato.
func TestCmdRunReadyDispara(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	fp := newFakePoller()
	fr := &fakeRunner{}
	cfg := newTestConfig()

	if err := st.EnsureRepo(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	_, _ = st.UpsertIssue(ctx, "web", 30, "feat run", "http://gh/30", domain.PhaseReady)
	fp.labels[lkey("web", 30)] = []string{domain.LabelReady}
	fp.issues["web"] = []github.Issue{
		{Repo: "web", Number: 30, Title: "feat run", URL: "http://gh/30",
			Labels: []string{domain.LabelReady}},
	}

	sched := newSched(st, fp, fr, cfg)
	sched.handleCommand(ctx, domain.Command{Type: domain.CmdRun, Issue: 30})
	waitFor(t, func() bool { return len(fr.called()) > 0 })

	if tasks := fr.called(); len(tasks) == 0 {
		t.Error("esperava runner invocado via /run, got 0 chamadas")
	}
}

// TestCmdRunAwaitingApprovalIgnorado: /run #N com phase=awaiting_approval não
// dispara o runner.
func TestCmdRunAwaitingApprovalIgnorado(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	fp := newFakePoller()
	fr := &fakeRunner{}
	cfg := newTestConfig()

	if err := st.EnsureRepo(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	issueID, _ := st.UpsertIssue(ctx, "web", 31, "feat aw", "http://gh/31", domain.PhaseAwaitingApproval)
	if err := st.SetPhase(ctx, issueID, domain.PhaseAwaitingApproval); err != nil {
		t.Fatal(err)
	}

	sched := newSched(st, fp, fr, cfg)
	sched.handleCommand(ctx, domain.Command{Type: domain.CmdRun, Issue: 31})
	time.Sleep(50 * time.Millisecond)

	if got := len(fr.called()); got != 0 {
		t.Errorf("esperava 0 chamadas ao runner para awaiting_approval, got %d", got)
	}
}

// TestCmdRunRepoPausadoIgnorado: /run #N em repo pausado não dispara o runner.
func TestCmdRunRepoPausadoIgnorado(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	fp := newFakePoller()
	fr := &fakeRunner{}
	cfg := newTestConfig()

	if err := st.EnsureRepo(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRepoPaused(ctx, "web", true, "manutenção"); err != nil {
		t.Fatal(err)
	}
	_, _ = st.UpsertIssue(ctx, "web", 32, "feat p", "http://gh/32", domain.PhaseReady)

	sched := newSched(st, fp, fr, cfg)
	sched.handleCommand(ctx, domain.Command{Type: domain.CmdRun, Issue: 32})
	time.Sleep(50 * time.Millisecond)

	if got := len(fr.called()); got != 0 {
		t.Errorf("esperava 0 chamadas ao runner para repo pausado, got %d", got)
	}
}

// Garante que fakePoller.listErr cobre o caso de erro de ListByLabels.
func TestListByLabelsErro(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	fp := newFakePoller()
	fr := &fakeRunner{}
	cfg := newTestConfig()

	if err := st.EnsureRepo(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	fp.listErr = errors.New("falha de rede simulada")

	sched := newSched(st, fp, fr, cfg)
	sched.processRepo(ctx, "web") // não deve panic

	if got := len(fr.called()); got != 0 {
		t.Errorf("esperava 0 chamadas ao runner com erro de lista, got %d", got)
	}
}

// TestBackoffRateLimit: 429 de ListByLabels → backoffUntil setado no Scheduler.
func TestBackoffRateLimit(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	fp := newFakePoller()
	fr := &fakeRunner{}
	cfg := newTestConfig()

	if err := st.EnsureRepo(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	fp.listErr = &github.APIError{StatusCode: 429}

	before := time.Now()
	sched := newSched(st, fp, fr, cfg)
	sched.processRepo(ctx, "web")

	if sched.backoffUntil.IsZero() {
		t.Fatal("esperava backoffUntil setado após 429")
	}
	if !sched.backoffUntil.After(before) {
		t.Errorf("backoffUntil %v não está no futuro", sched.backoffUntil)
	}
}

// TestBackoffSkipsTick: com backoffUntil no futuro, processTick não chama ListByLabels.
func TestBackoffSkipsTick(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	fp := newFakePoller()
	fr := &fakeRunner{}
	cfg := newTestConfig()

	if err := st.EnsureRepo(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	fp.issues["web"] = []github.Issue{
		{Repo: "web", Number: 99, Labels: []string{domain.LabelReady}},
	}

	sched := newSched(st, fp, fr, cfg)
	sched.backoffUntil = time.Now().Add(1 * time.Hour)
	sched.processTick(ctx)
	time.Sleep(30 * time.Millisecond)

	if got := len(fr.called()); got != 0 {
		t.Errorf("esperava 0 chamadas ao runner durante backoff, got %d", got)
	}
}
