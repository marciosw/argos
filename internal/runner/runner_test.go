package runner

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/marciomacedo/argos/internal/config"
	"github.com/marciomacedo/argos/internal/domain"
	"github.com/marciomacedo/argos/internal/github"
	"github.com/marciomacedo/argos/internal/store"
	"github.com/marciomacedo/argos/internal/telegram"
)

// ─── fakes ────────────────────────────────────────────────────────────────────

// fakeCmd emite linhas de stream-json canned por invocação.
type fakeCmd struct {
	mu          sync.Mutex
	calls       int
	linesByCall [][]string // por chamada; se exceder, repete a última
	exitCode    int
	stderr      string
	runErr      error
	dirs        []string
	stdins      []string
}

func (f *fakeCmd) Run(_ context.Context, spec commandSpec, onLine lineFunc) (commandResult, error) {
	f.mu.Lock()
	idx := f.calls
	f.calls++
	f.dirs = append(f.dirs, spec.Dir)
	f.stdins = append(f.stdins, spec.Stdin)
	var lines []string
	switch {
	case idx < len(f.linesByCall):
		lines = f.linesByCall[idx]
	case len(f.linesByCall) > 0:
		lines = f.linesByCall[len(f.linesByCall)-1]
	}
	f.mu.Unlock()
	for _, l := range lines {
		onLine(l)
	}
	return commandResult{ExitCode: f.exitCode, Stderr: f.stderr}, f.runErr
}

func (f *fakeCmd) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeGit struct {
	ensured            []string
	pushed             []string
	commits            int
	ensureErr, pushErr error
}

func (f *fakeGit) EnsureBranch(_ context.Context, _, branch string) error {
	f.ensured = append(f.ensured, branch)
	return f.ensureErr
}
func (f *fakeGit) Push(_ context.Context, _, branch string) error {
	f.pushed = append(f.pushed, branch)
	return f.pushErr
}
func (f *fakeGit) CountCommits(_ context.Context, _, _, _ string) (int, error) {
	return f.commits, nil
}

type fakeGitHub struct {
	pr       github.PullRequest
	openErr  error
	opened   []github.OpenPRInput
	comments []string
	issue    github.Issue
	issueErr error
}

func (f *fakeGitHub) OpenPR(_ context.Context, _ string, in github.OpenPRInput) (github.PullRequest, error) {
	f.opened = append(f.opened, in)
	return f.pr, f.openErr
}
func (f *fakeGitHub) Comment(_ context.Context, _ string, _ int, body string) error {
	f.comments = append(f.comments, body)
	return nil
}
func (f *fakeGitHub) GetIssue(_ context.Context, _ string, _ int) (github.Issue, error) {
	return f.issue, f.issueErr
}

// fakeWorkspace devolve um caminho de checkout pré-fabricado (o tempdir do teste).
type fakeWorkspace struct {
	path  string
	repos []string
	err   error
}

func (f *fakeWorkspace) Prepare(_ context.Context, repo string) (string, error) {
	f.repos = append(f.repos, repo)
	return f.path, f.err
}

type fakeNotifier struct {
	approvals   []telegram.ApprovalRequest
	prs         []string
	errs        []error
	approvalErr error
}

func (f *fakeNotifier) SendApprovalRequest(_ context.Context, in telegram.ApprovalRequest) error {
	f.approvals = append(f.approvals, in)
	return f.approvalErr
}
func (f *fakeNotifier) NotifyPR(_ context.Context, _ string, _ int, prURL string) error {
	f.prs = append(f.prs, prURL)
	return nil
}
func (f *fakeNotifier) NotifyError(_ context.Context, _ string, _ int, _ string, taskErr error) error {
	f.errs = append(f.errs, taskErr)
	return nil
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

func newTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, _ := config.Load("")
	cfg.Telegram.AllowedChatIDs = []int64{111}
	return cfg
}

// usageLine produz uma linha assistant + result com o mesmo usage (espelha o
// schema real, em que o result repete o usage acumulado).
func streamLines(u Usage, withResult bool) []string {
	asst := fmt.Sprintf(
		`{"type":"assistant","session_id":"sess-abc","message":{"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d}}}`,
		u.InputTokens, u.OutputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens)
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"sess-abc","model":"claude-test"}`,
		asst,
	}
	if withResult {
		result := fmt.Sprintf(
			`{"type":"result","subtype":"success","is_error":false,"session_id":"sess-abc","usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d}}`,
			u.InputTokens, u.OutputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens)
		lines = append(lines, result)
	}
	return lines
}

func createIssue(t *testing.T, st *store.Store, repo string, num int, title string, phase domain.Phase) int64 {
	t.Helper()
	id, err := st.UpsertIssue(context.Background(), repo, num, title, "http://gh/"+title, phase)
	if err != nil {
		t.Fatalf("UpsertIssue: %v", err)
	}
	return id
}

// ─── parser ───────────────────────────────────────────────────────────────────

func TestParserAccumulatesLastUsage(t *testing.T) {
	p := &streamParser{}
	for _, l := range streamLines(Usage{InputTokens: 10, CacheReadInputTokens: 20, CacheCreationInputTokens: 5}, true) {
		p.feed(l)
	}
	if p.sessionID != "sess-abc" {
		t.Errorf("session_id esperado sess-abc, got %q", p.sessionID)
	}
	if !p.sawResult {
		t.Error("esperava sawResult=true")
	}
	if p.lastUsage.InputTokens != 10 || p.lastUsage.CacheReadInputTokens != 20 || p.lastUsage.CacheCreationInputTokens != 5 {
		t.Errorf("usage acumulado inesperado: %+v", p.lastUsage)
	}
}

func TestParserToleratesMalformedLines(t *testing.T) {
	p := &streamParser{}
	p.feed(`{"type":"assistant","message":{"usage":{"input_tokens":7}}}`)
	p.feed(`isto não é json`)             // tolerado
	p.feed(`{"type":"rate_limit_event"}`) // tipo desconhecido, sem usage
	p.feed(`{"type":"result","is_error":false,"usage":{"input_tokens":7}}`)
	if p.lastUsage.InputTokens != 7 {
		t.Errorf("malformed/desconhecido não deviam alterar usage; got %+v", p.lastUsage)
	}
	if !p.sawResult {
		t.Error("esperava sawResult=true após o result")
	}
}

func TestParserNoResultIsFailure(t *testing.T) {
	p := &streamParser{}
	for _, l := range streamLines(Usage{InputTokens: 5}, false) {
		p.feed(l)
	}
	if p.sawResult {
		t.Error("stream sem result deveria deixar sawResult=false")
	}
}

func TestContextUsage(t *testing.T) {
	// (input + cache_read + cache_creation) / window
	u := Usage{InputTokens: 1000, CacheReadInputTokens: 120000, CacheCreationInputTokens: 9000}
	got := contextUsage(u, 200000) // 130000/200000 = 0.65
	if got < 0.649 || got > 0.651 {
		t.Errorf("contextUsage esperado ~0.65, got %v", got)
	}
	if contextUsage(u, 0) != 0 {
		t.Error("janela inválida deveria retornar 0")
	}

	threshold := 0.65
	below := contextUsage(Usage{InputTokens: 100}, 200000)
	if below > threshold {
		t.Errorf("uso baixo (%v) não deveria exceder o limiar", below)
	}
	above := contextUsage(Usage{CacheReadInputTokens: 180000}, 200000) // 0.9
	if above <= threshold {
		t.Errorf("uso alto (%v) deveria exceder o limiar", above)
	}
}

// ─── Run: documentation ─────────────────────────────────────────────────────────

func TestRunDocumentation(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	cfg := newTestConfig(t)
	repoPath := t.TempDir()

	// Simula o design.md que o agente produziria, com seções para o digest.
	specDir := filepath.Join(repoPath, "docs", "specs", "issue-42")
	if err := os.MkdirAll(specDir, 0o755); err != nil {
		t.Fatal(err)
	}
	design := "# Design\n\n## Objetivos\nFazer X e Y.\n\n## Componentes\nA e B.\n\n## Decisões técnicas\nUsar Z.\n\n## Riscos\nPode falhar W.\n"
	if err := os.WriteFile(filepath.Join(specDir, "design.md"), []byte(design), 0o644); err != nil {
		t.Fatal(err)
	}

	createIssue(t, st, "web", 42, "feat doc", domain.PhaseDocumentation)

	cmd := &fakeCmd{linesByCall: [][]string{streamLines(Usage{InputTokens: 100}, true)}}
	git := &fakeGit{}
	gh := &fakeGitHub{}
	nt := &fakeNotifier{}
	r := newWithDeps(cfg, st, cmd, git, gh, nt, &fakeWorkspace{path: repoPath})

	task := domain.Task{Repo: "web", Issue: 42, Phase: domain.PhaseDocumentation,
		Model: "claude-test", ContextWindow: 200000}

	if err := r.Run(ctx, task); err != nil {
		t.Fatalf("Run documentation: %v", err)
	}

	if len(nt.approvals) != 1 {
		t.Fatalf("esperava 1 SendApprovalRequest, got %d", len(nt.approvals))
	}
	req := nt.approvals[0]
	if req.ChatID != 111 {
		t.Errorf("chat_id esperado 111, got %d", req.ChatID)
	}
	if req.Data.Objectives == notInformed || !strings.Contains(req.Data.Objectives, "Fazer X") {
		t.Errorf("digest.Objectives não extraído: %q", req.Data.Objectives)
	}
	foundDesign := false
	for _, f := range req.SpecFiles {
		if filepath.Base(f) == "design.md" {
			foundDesign = true
		}
	}
	if !foundDesign {
		t.Errorf("design.md não está entre os SpecFiles: %v", req.SpecFiles)
	}

	// SpecDir gravado no store.
	iss, _ := st.GetIssue(ctx, "web", 42)
	if iss.SpecDir != specDir {
		t.Errorf("SpecDir esperado %q, got %q", specDir, iss.SpecDir)
	}

	// Uma sessão registrada e concluída.
	if n := sessionCount(t, st); n != 1 {
		t.Errorf("esperava 1 sessão, got %d", n)
	}
	if cmd.count() != 1 {
		t.Errorf("esperava 1 invocação do subprocesso, got %d", cmd.count())
	}
	// Prompt foi enviado por stdin (não vazio).
	if strings.TrimSpace(cmd.stdins[0]) == "" {
		t.Error("prompt deveria ter sido enviado via stdin")
	}
}

// ─── workspace + context.md ──────────────────────────────────────────────────────

// TestRunUsesWorkspacePath garante que o runner pede o checkout ao workspace e
// usa o caminho devolvido como working dir do subprocesso (RepoPath fiado aqui).
func TestRunUsesWorkspacePath(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	cfg := newTestConfig(t)
	repoPath := t.TempDir()

	createIssue(t, st, "web", 42, "feat ws", domain.PhaseDocumentation)

	cmd := &fakeCmd{linesByCall: [][]string{streamLines(Usage{InputTokens: 100}, true)}}
	ws := &fakeWorkspace{path: repoPath}
	gh := &fakeGitHub{issue: github.Issue{Body: "qualquer"}}
	r := newWithDeps(cfg, st, cmd, &fakeGit{}, gh, &fakeNotifier{}, ws)

	// Task SEM RepoPath: deve ser resolvido pelo workspace.
	task := domain.Task{Repo: "web", Issue: 42, Phase: domain.PhaseDocumentation,
		Model: "claude-test", ContextWindow: 200000}

	if err := r.Run(ctx, task); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(ws.repos) != 1 || ws.repos[0] != "web" {
		t.Errorf("esperava Prepare(\"web\"), got %v", ws.repos)
	}
	if len(cmd.dirs) != 1 || cmd.dirs[0] != repoPath {
		t.Errorf("subprocesso deveria rodar em %q (do workspace), got %v", repoPath, cmd.dirs)
	}
}

// TestRunSeedsContext garante que context.md é semeado com o corpo da issue na
// fase de documentação.
func TestRunSeedsContext(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	cfg := newTestConfig(t)
	repoPath := t.TempDir()

	createIssue(t, st, "web", 42, "feat ctx", domain.PhaseDocumentation)

	cmd := &fakeCmd{linesByCall: [][]string{streamLines(Usage{InputTokens: 100}, true)}}
	gh := &fakeGitHub{issue: github.Issue{Body: "Permitir login social com Google."}}
	r := newWithDeps(cfg, st, cmd, &fakeGit{}, gh, &fakeNotifier{}, &fakeWorkspace{path: repoPath})

	task := domain.Task{Repo: "web", Issue: 42, Phase: domain.PhaseDocumentation,
		Model: "claude-test", ContextWindow: 200000}

	if err := r.Run(ctx, task); err != nil {
		t.Fatalf("Run: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(repoPath, "docs", "specs", "issue-42", "context.md"))
	if err != nil {
		t.Fatalf("ler context.md: %v", err)
	}
	if !strings.Contains(string(data), "Permitir login social com Google.") {
		t.Errorf("context.md não contém o corpo da issue:\n%s", data)
	}
}

// TestSeedContextPreservesExisting garante que conteúdo prévio (ex.: motivo de
// /reject) NÃO é sobrescrito pela semeadura.
func TestSeedContextPreservesExisting(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	cfg := newTestConfig(t)
	repoPath := t.TempDir()

	specDir := filepath.Join(repoPath, "docs", "specs", "issue-42")
	if err := os.MkdirAll(specDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const prev = "## /reject\nMotivo: faltou tratar erro de rede."
	if err := os.WriteFile(filepath.Join(specDir, "context.md"), []byte(prev), 0o644); err != nil {
		t.Fatal(err)
	}

	createIssue(t, st, "web", 42, "feat ctx", domain.PhaseDocumentation)

	cmd := &fakeCmd{linesByCall: [][]string{streamLines(Usage{InputTokens: 100}, true)}}
	gh := &fakeGitHub{issue: github.Issue{Body: "corpo novo da issue"}}
	r := newWithDeps(cfg, st, cmd, &fakeGit{}, gh, &fakeNotifier{}, &fakeWorkspace{path: repoPath})

	task := domain.Task{Repo: "web", Issue: 42, Phase: domain.PhaseDocumentation,
		Model: "claude-test", ContextWindow: 200000}

	if err := r.Run(ctx, task); err != nil {
		t.Fatalf("Run: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(specDir, "context.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != prev {
		t.Errorf("context.md foi sobrescrito; esperava preservar %q, got %q", prev, string(data))
	}
}

// ─── Run: doing ─────────────────────────────────────────────────────────────────

func TestRunCoding(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	cfg := newTestConfig(t)
	repoPath := t.TempDir()

	createIssue(t, st, "web", 7, "feat code", domain.PhaseDoing)

	cmd := &fakeCmd{linesByCall: [][]string{streamLines(Usage{InputTokens: 100}, true)}}
	git := &fakeGit{commits: 3}
	gh := &fakeGitHub{pr: github.PullRequest{Number: 9, URL: "http://gh/pull/9"}}
	nt := &fakeNotifier{}
	r := newWithDeps(cfg, st, cmd, git, gh, nt, &fakeWorkspace{path: repoPath})

	task := domain.Task{Repo: "web", Issue: 7, Phase: domain.PhaseDoing,
		Model: "claude-test", ContextWindow: 200000}

	if err := r.Run(ctx, task); err != nil {
		t.Fatalf("Run coding: %v", err)
	}

	wantBranch := cfg.AgentBranchPrefix + "7" // "agent/issue-7"
	if len(git.ensured) != 1 || git.ensured[0] != wantBranch {
		t.Errorf("EnsureBranch esperado [%s], got %v", wantBranch, git.ensured)
	}
	if len(git.pushed) != 1 || git.pushed[0] != wantBranch {
		t.Errorf("Push esperado [%s], got %v", wantBranch, git.pushed)
	}
	if len(gh.opened) != 1 {
		t.Fatalf("esperava 1 OpenPR, got %d", len(gh.opened))
	}
	if gh.opened[0].Branch != wantBranch || gh.opened[0].Base != cfg.BaseBranch {
		t.Errorf("OpenPRInput inesperado: %+v", gh.opened[0])
	}
	if !strings.Contains(gh.opened[0].Body, "Closes #7") {
		t.Errorf("corpo do PR deveria conter Closes #7: %q", gh.opened[0].Body)
	}
	if len(nt.prs) != 1 || nt.prs[0] != "http://gh/pull/9" {
		t.Errorf("NotifyPR esperado com a URL do PR, got %v", nt.prs)
	}
	if len(gh.comments) != 1 || !strings.Contains(gh.comments[0], "http://gh/pull/9") {
		t.Errorf("comentário na issue esperado com a URL, got %v", gh.comments)
	}

	iss, _ := st.GetIssue(ctx, "web", 7)
	if iss.PRURL != "http://gh/pull/9" {
		t.Errorf("PRURL esperado, got %q", iss.PRURL)
	}
}

// ─── Run: reinício por contexto ──────────────────────────────────────────────────

func TestRunResumesOnContext(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	cfg := newTestConfig(t)
	repoPath := t.TempDir()

	createIssue(t, st, "web", 5, "feat resume", domain.PhaseDocumentation)

	// 1ª sessão: uso ALTO (0.9 > 0.65) → reinício. 2ª sessão: uso baixo → fim.
	high := streamLines(Usage{CacheReadInputTokens: 180000}, true)
	low := streamLines(Usage{InputTokens: 100}, true)
	cmd := &fakeCmd{linesByCall: [][]string{high, low}}
	r := newWithDeps(cfg, st, cmd, &fakeGit{}, &fakeGitHub{}, &fakeNotifier{}, &fakeWorkspace{path: repoPath})

	task := domain.Task{Repo: "web", Issue: 5, Phase: domain.PhaseDocumentation,
		Model: "claude-test", ContextWindow: 200000}

	if err := r.Run(ctx, task); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if cmd.count() != 2 {
		t.Fatalf("esperava 2 invocações (reinício), got %d", cmd.count())
	}
	if n := sessionCount(t, st); n != 2 {
		t.Fatalf("esperava 2 sessões, got %d", n)
	}

	// 1ª sessão: status resumed, sem resumed_from. 2ª: completed, resumed_from = id da 1ª.
	rows, err := st.DB().QueryContext(ctx,
		`SELECT id, status, resumed_from FROM sessions ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type srow struct {
		id          int64
		status      string
		resumedFrom sql.NullInt64
	}
	var got []srow
	for rows.Next() {
		var s srow
		if err := rows.Scan(&s.id, &s.status, &s.resumedFrom); err != nil {
			t.Fatal(err)
		}
		got = append(got, s)
	}
	if got[0].status != string(domain.SessionResumed) {
		t.Errorf("1ª sessão deveria ter status resumed, got %q", got[0].status)
	}
	if got[1].status != string(domain.SessionCompleted) {
		t.Errorf("2ª sessão deveria ter status completed, got %q", got[1].status)
	}
	if !got[1].resumedFrom.Valid || got[1].resumedFrom.Int64 != got[0].id {
		t.Errorf("2ª sessão deveria apontar resumed_from = %d, got %+v", got[0].id, got[1].resumedFrom)
	}

	// audit_log registra o reinício.
	var auditN int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(1) FROM audit_log WHERE event = 'resumed_due_to_context'`).Scan(&auditN); err != nil {
		t.Fatal(err)
	}
	if auditN != 1 {
		t.Errorf("esperava 1 evento resumed_due_to_context, got %d", auditN)
	}
}

// ─── Run: erro (exit ≠ 0) ────────────────────────────────────────────────────────

func TestRunErrorPreservesProgress(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	cfg := newTestConfig(t)
	repoPath := t.TempDir()

	// progress.md pré-existente deve ser preservado (não sobrescrito nem apagado).
	specDir := filepath.Join(repoPath, "docs", "specs", "issue-3")
	if err := os.MkdirAll(specDir, 0o755); err != nil {
		t.Fatal(err)
	}
	progressPath := filepath.Join(specDir, "progress.md")
	if err := os.WriteFile(progressPath, []byte("ESTADO PRESERVADO"), 0o644); err != nil {
		t.Fatal(err)
	}

	createIssue(t, st, "web", 3, "feat erro", domain.PhaseDocumentation)

	cmd := &fakeCmd{exitCode: 1, stderr: "boom", linesByCall: [][]string{streamLines(Usage{InputTokens: 1}, false)}}
	nt := &fakeNotifier{}
	r := newWithDeps(cfg, st, cmd, &fakeGit{}, &fakeGitHub{}, nt, &fakeWorkspace{path: repoPath})

	task := domain.Task{Repo: "web", Issue: 3, Phase: domain.PhaseDocumentation,
		Model: "claude-test", ContextWindow: 200000}

	err := r.Run(ctx, task)
	if err == nil {
		t.Fatal("esperava erro de exit ≠ 0")
	}

	// progress.md preservado.
	data, readErr := os.ReadFile(progressPath)
	if readErr != nil {
		t.Fatalf("progress.md deveria existir: %v", readErr)
	}
	if string(data) != "ESTADO PRESERVADO" {
		t.Errorf("progress.md foi alterado: %q", string(data))
	}

	// NotifyError chamado.
	if len(nt.errs) != 1 {
		t.Errorf("esperava 1 NotifyError, got %d", len(nt.errs))
	}
	// Sessão marcada como failed.
	var failedN int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(1) FROM sessions WHERE status = ?`, string(domain.SessionFailed)).Scan(&failedN); err != nil {
		t.Fatal(err)
	}
	if failedN != 1 {
		t.Errorf("esperava 1 sessão failed, got %d", failedN)
	}
}

func sessionCount(t *testing.T, st *store.Store) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(1) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
