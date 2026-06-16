// Package runner implementa o TaskRunner: invoca o Claude Code CLI como
// subprocesso (uma instância por tarefa), parseia o output stream-json, monitora
// o uso da janela de contexto e gerencia a retomada via progress.md.
//
// O runner satisfaz scheduler.TaskRunner (Run(ctx, domain.Task) error). Como o
// scheduler já está congelado e seu lifecycle.go faz as transições de label a
// partir do sucesso/erro de Run, os efeitos de fim de fase (enviar o digest de
// aprovação no Telegram na fase documentation; abrir o PR + notificar na fase
// doing) acontecem DENTRO do runner, antes de Run retornar.
//
// Ver docs/specs/task_runner.md.
package runner

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/marciomacedo/argos/internal/config"
	"github.com/marciomacedo/argos/internal/domain"
	"github.com/marciomacedo/argos/internal/github"
	"github.com/marciomacedo/argos/internal/store"
	"github.com/marciomacedo/argos/internal/telegram"
)

// defaultMaxSessions limita as retomadas por contexto numa mesma fase, evitando
// loop infinito caso o uso nunca fique abaixo do limiar.
const defaultMaxSessions = 8

// prClient é o subconjunto do github.Poller usado pelo runner (abrir PR,
// comentar na issue e ler o corpo da issue para semear context.md).
// github.Poller o satisfaz.
type prClient interface {
	OpenPR(ctx context.Context, repo string, in github.OpenPRInput) (github.PullRequest, error)
	Comment(ctx context.Context, repo string, issue int, body string) error
	GetIssue(ctx context.Context, repo string, issue int) (github.Issue, error)
}

// workspacePreparer garante o checkout local do repo-alvo e devolve seu caminho
// absoluto. workspace.Manager o satisfaz (interface local p/ evitar acoplamento
// de pacote, no padrão das demais dependências do runner).
type workspacePreparer interface {
	Prepare(ctx context.Context, repo string) (string, error)
}

// notifier é o subconjunto do telegram.Gateway usado pelo runner. A interface
// telegram.Gateway o satisfaz.
type notifier interface {
	SendApprovalRequest(ctx context.Context, in telegram.ApprovalRequest) error
	NotifyPR(ctx context.Context, repo string, issueNum int, prURL string) error
	NotifyError(ctx context.Context, repo string, issueNum int, phase string, taskErr error) error
}

// Runner é a implementação do TaskRunner.
type Runner struct {
	cfg    *config.Config
	store  *store.Store
	cmd    commandRunner
	git    gitOps
	gh     prClient
	notify notifier
	ws     workspacePreparer

	maxSessions int
}

// New cria um Runner com as implementações reais de subprocesso e git. As
// dependências externas (store, github, telegram, workspace, config) são
// injetadas. ws garante o checkout local do repo-alvo (RepoPath).
func New(cfg *config.Config, st *store.Store, gh prClient, notify notifier, ws workspacePreparer) *Runner {
	return newWithDeps(cfg, st, execRunner{killGrace: 10 * time.Second}, execGit{}, gh, notify, ws)
}

// newWithDeps é o construtor interno: injeta TODAS as dependências (usado nos
// testes com fakes de subprocesso/git/workspace).
func newWithDeps(cfg *config.Config, st *store.Store, cmd commandRunner, git gitOps, gh prClient, notify notifier, ws workspacePreparer) *Runner {
	return &Runner{
		cfg:         cfg,
		store:       st,
		cmd:         cmd,
		git:         git,
		gh:          gh,
		notify:      notify,
		ws:          ws,
		maxSessions: defaultMaxSessions,
	}
}

// Run despacha a tarefa conforme a fase (documentation vs. doing). Satisfaz
// scheduler.TaskRunner.
func (r *Runner) Run(ctx context.Context, task domain.Task) error {
	iss, err := r.store.GetIssue(ctx, task.Repo, task.Issue)
	if err != nil {
		return fmt.Errorf("runner: issue %s#%d não encontrada no store: %w", task.Repo, task.Issue, err)
	}

	log := slog.With("repo", task.Repo, "issue", task.Issue, "phase", string(task.Phase))

	// Resolve o checkout local do repo-alvo (clone/fetch idempotente) e usa-o
	// como working dir. O scheduler (congelado) cria a Task sem RepoPath; é aqui
	// que ele é fiado. Ver task_runner.md §11.4.
	if r.ws != nil {
		path, err := r.ws.Prepare(ctx, task.Repo)
		if err != nil {
			err = fmt.Errorf("runner: preparar checkout de %s: %w", task.Repo, err)
			r.notifyError(ctx, task, err)
			return err
		}
		task.RepoPath = path
		log.Info("runner: checkout preparado", "path", path)
	}

	switch task.Phase {
	case domain.PhaseDocumentation:
		return r.runDocumentation(ctx, task, iss, log)
	case domain.PhaseDoing:
		return r.runCoding(ctx, task, iss, log)
	default:
		return fmt.Errorf("runner: fase não suportada: %q", task.Phase)
	}
}

// repoPath resolve o checkout local do repo-alvo. O caller (Scheduler) deve
// preencher task.RepoPath; enquanto isso não estiver fiado, cai no diretório
// corrente. Ver task_runner.md §11.4 (pendência).
func (r *Runner) repoPath(task domain.Task) string {
	if task.RepoPath != "" {
		return task.RepoPath
	}
	return "."
}

func (r *Runner) specDir(task domain.Task) string {
	return filepath.Join(r.repoPath(task), "docs", "specs", fmt.Sprintf("issue-%d", task.Issue))
}

// runDocumentation executa a fase de documentação: garante o scaffold de specs,
// roda o(s) subprocesso(s) e, ao concluir, envia o digest de aprovação ao
// humano via Telegram (o scheduler então transita para awaiting_approval).
func (r *Runner) runDocumentation(ctx context.Context, task domain.Task, iss domain.Issue, log *slog.Logger) error {
	specDir := r.specDir(task)
	if err := ensureSpecScaffold(specDir); err != nil {
		return fmt.Errorf("runner: scaffold de specs: %w", err)
	}

	// Semeia context.md com o corpo da issue (objetivo de negócio) na fase de
	// documentação. Best-effort: não bloqueia a fase e nunca sobrescreve
	// conteúdo já existente (pode conter motivos de /reject). Ver task_runner.md §6.
	r.seedContext(ctx, task, specDir, log)

	if err := r.runPhaseLoop(ctx, task, iss, "", log); err != nil {
		r.notifyError(ctx, task, err)
		return err
	}

	if err := r.store.SetSpecDir(ctx, iss.ID, specDir); err != nil {
		log.Warn("runner: SetSpecDir", "err", err)
	}

	// Monta e envia o digest de aprovação.
	digest := buildDigest(task.Repo, task.Issue, iss.Title, filepath.Join(specDir, "design.md"))
	chatID := r.approvalChatID()
	if chatID == 0 {
		log.Warn("runner: sem allowed_chat_ids — digest de aprovação não enviado")
		return nil
	}
	req := telegram.ApprovalRequest{
		ChatID:    chatID,
		Data:      digest,
		SpecFiles: existingSpecFiles(specDir),
	}
	if err := r.notify.SendApprovalRequest(ctx, req); err != nil {
		// Falha ao notificar não invalida a documentação produzida; loga e segue.
		log.Error("runner: SendApprovalRequest", "err", err)
	}
	log.Info("runner: documentação concluída, digest enviado")
	return nil
}

// runCoding executa a fase de codificação: garante a branch, roda o(s)
// subprocesso(s) (que fazem os commits), empurra a branch, abre o PR e notifica.
func (r *Runner) runCoding(ctx context.Context, task domain.Task, iss domain.Issue, log *slog.Logger) error {
	dir := r.repoPath(task)
	branch := fmt.Sprintf("%s%d", r.cfg.AgentBranchPrefix, task.Issue)

	if err := r.git.EnsureBranch(ctx, dir, branch); err != nil {
		err = fmt.Errorf("runner: garantir branch %q: %w", branch, err)
		r.notifyError(ctx, task, err)
		return err
	}

	if err := r.runPhaseLoop(ctx, task, iss, branch, log); err != nil {
		r.notifyError(ctx, task, err)
		return err
	}

	if err := r.git.Push(ctx, dir, branch); err != nil {
		err = fmt.Errorf("runner: push branch %q: %w", branch, err)
		r.notifyError(ctx, task, err)
		return err
	}

	commits, err := r.git.CountCommits(ctx, dir, r.cfg.BaseBranch, branch)
	if err != nil {
		log.Warn("runner: CountCommits", "err", err)
	}

	pr, err := r.gh.OpenPR(ctx, task.Repo, github.OpenPRInput{
		Issue:  task.Issue,
		Branch: branch,
		Base:   r.cfg.BaseBranch,
		Title:  fmt.Sprintf("%s (issue #%d)", firstNonEmpty(iss.Title, "Mudanças do agente"), task.Issue),
		Body:   fmt.Sprintf("Closes #%d\n\nPR aberto automaticamente pelo Argos (%d commit(s)).", task.Issue, commits),
	})
	if err != nil {
		err = fmt.Errorf("runner: abrir PR: %w", err)
		r.notifyError(ctx, task, err)
		return err
	}

	if err := r.store.SetPRURL(ctx, iss.ID, pr.URL); err != nil {
		log.Warn("runner: SetPRURL", "err", err)
	}
	if err := r.gh.Comment(ctx, task.Repo, task.Issue, "PR aberto: "+pr.URL); err != nil {
		log.Warn("runner: comentar na issue", "err", err)
	}
	if err := r.notify.NotifyPR(ctx, task.Repo, task.Issue, pr.URL); err != nil {
		log.Warn("runner: NotifyPR", "err", err)
	}
	log.Info("runner: codificação concluída, PR aberto", "pr_url", pr.URL, "commits", commits)
	return nil
}

// runPhaseLoop executa o subprocesso `claude` para a fase, repetindo numa NOVA
// sessão (reset de contexto via progress.md) sempre que o uso da janela superar
// o limiar. branch != "" indica fase de codificação (define o template).
//
// Heurística de "bloco de tasks" (spec §11.3): cada invocação do subprocesso é
// um bloco. Ao seu término, checa-se o uso de contexto. Uso abaixo do limiar é
// interpretado como fase concluída dentro do orçamento; uso acima dispara uma
// retomada com contexto reiniciado. O cap maxSessions evita loop infinito.
func (r *Runner) runPhaseLoop(ctx context.Context, task domain.Task, iss domain.Issue, branch string, log *slog.Logger) error {
	window := task.ContextWindow
	if window <= 0 {
		window = r.cfg.Model.ContextWindow
	}
	threshold := r.cfg.Model.ContextThreshold
	if threshold <= 0 {
		threshold = config.DefaultContextThreshold
	}

	pData := promptData{
		Repo:       task.Repo,
		Issue:      task.Issue,
		SpecDir:    filepath.Join("docs", "specs", fmt.Sprintf("issue-%d", task.Issue)),
		Branch:     branch,
		BaseBranch: r.cfg.BaseBranch,
	}

	var prevSessionID int64
	var lastPct float64

	for n := 1; n <= r.maxSessions; n++ {
		resumed := n > 1
		prompt := r.buildPrompt(task.Phase, branch, resumed, pData)

		sid, err := r.store.CreateSession(ctx, store.SessionInput{
			IssueID:     iss.ID,
			Phase:       task.Phase,
			Model:       task.Model,
			ResumedFrom: prevSessionID,
		})
		if err != nil {
			return fmt.Errorf("runner: criar sessão: %w", err)
		}

		parser := &streamParser{}
		spec := commandSpec{
			Name:  "claude",
			Args:  claudeArgs(task.Model),
			Dir:   r.repoPath(task),
			Stdin: prompt,
		}
		res, runErr := r.cmd.Run(ctx, spec, parser.feed)

		if parser.sessionID != "" {
			if err := r.store.SetClaudeSessionID(ctx, sid, parser.sessionID); err != nil {
				log.Warn("runner: SetClaudeSessionID", "err", err)
			}
		}
		lastPct = contextUsage(parser.lastUsage, window)

		// Falhas de execução / contrato do stream → erro (spec §10).
		if runErr != nil {
			_ = r.store.EndSession(ctx, sid, domain.SessionFailed, lastPct)
			return fmt.Errorf("runner: subprocesso claude (sessão %d): %w", n, runErr)
		}
		if res.ExitCode != 0 {
			_ = r.store.EndSession(ctx, sid, domain.SessionFailed, lastPct)
			return fmt.Errorf("runner: claude saiu com código %d: %s", res.ExitCode, truncate(res.Stderr, 500))
		}
		if !parser.sawResult {
			_ = r.store.EndSession(ctx, sid, domain.SessionFailed, lastPct)
			return fmt.Errorf("runner: stream-json encerrou sem evento result (sessão %d)", n)
		}
		if parser.resultErr {
			_ = r.store.EndSession(ctx, sid, domain.SessionFailed, lastPct)
			return fmt.Errorf("runner: claude reportou is_error no result (sessão %d)", n)
		}

		log.Info("runner: sessão concluída", "session", n, "claude_session_id", parser.sessionID, "context_pct", lastPct)

		// Checagem de janela de contexto (spec §4–§5).
		if lastPct > threshold {
			if err := r.store.EndSession(ctx, sid, domain.SessionResumed, lastPct); err != nil {
				log.Warn("runner: EndSession resumed", "err", err)
			}
			detail := fmt.Sprintf(`{"context_pct":%.4f,"threshold":%.4f,"session":%d}`, lastPct, threshold, n)
			if err := r.store.Audit(ctx, iss.ID, task.Repo, "resumed_due_to_context", detail); err != nil {
				log.Warn("runner: audit resumed_due_to_context", "err", err)
			}
			log.Info("runner: uso de contexto acima do limiar — reiniciando sessão",
				"context_pct", lastPct, "threshold", threshold)
			prevSessionID = sid
			continue
		}

		// Uso dentro do orçamento → fase concluída.
		if err := r.store.EndSession(ctx, sid, domain.SessionCompleted, lastPct); err != nil {
			log.Warn("runner: EndSession completed", "err", err)
		}
		return nil
	}

	// Esgotou o cap de sessões sem ficar abaixo do limiar. O humano ainda gateia
	// (aprovação da spec / review do PR), então prosseguimos com aviso.
	log.Warn("runner: limite de sessões atingido sem cair abaixo do limiar",
		"max_sessions", r.maxSessions, "context_pct", lastPct)
	return nil
}

// buildPrompt escolhe o template conforme a fase e se é retomada.
func (r *Runner) buildPrompt(phase domain.Phase, branch string, resumed bool, d promptData) string {
	if resumed {
		return renderPrompt(resumeTemplate, d)
	}
	if phase == domain.PhaseDoing {
		return renderPrompt(codingTemplate, d)
	}
	return renderPrompt(docTemplate, d)
}

func (r *Runner) approvalChatID() int64 {
	if len(r.cfg.Telegram.AllowedChatIDs) > 0 {
		return r.cfg.Telegram.AllowedChatIDs[0]
	}
	return 0
}

func (r *Runner) notifyError(ctx context.Context, task domain.Task, taskErr error) {
	if err := r.notify.NotifyError(ctx, task.Repo, task.Issue, string(task.Phase), taskErr); err != nil {
		slog.Warn("runner: NotifyError", "repo", task.Repo, "issue", task.Issue, "err", err)
	}
}

// claudeArgs monta os argumentos do CLI (flags reais confirmadas via
// `claude --help`). O prompt vai por stdin (não por -p), evitando o limite de
// tamanho de argumento para prompts grandes (spec §11.2).
func claudeArgs(model string) []string {
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--dangerously-skip-permissions",
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	return args
}

// ensureSpecScaffold garante o diretório e os 4 arquivos de spec (vazios se
// ausentes). Não sobrescreve arquivos existentes (spec §6).
func ensureSpecScaffold(specDir string) error {
	if err := os.MkdirAll(specDir, 0o755); err != nil {
		return err
	}
	for _, name := range specFileNames {
		p := filepath.Join(specDir, name)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			if err := os.WriteFile(p, nil, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// seedContext semeia context.md com o corpo da issue. Só escreve se o arquivo
// estiver vazio (preserva conteúdo prévio, ex.: motivos de /reject). Best-effort:
// falhas são logadas e não abortam a fase.
func (r *Runner) seedContext(ctx context.Context, task domain.Task, specDir string, log *slog.Logger) {
	path := filepath.Join(specDir, "context.md")
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		// Já tem conteúdo — não sobrescreve.
		return
	}
	iss, err := r.gh.GetIssue(ctx, task.Repo, task.Issue)
	if err != nil {
		log.Warn("runner: GetIssue para semear context.md", "err", err)
		return
	}
	body := strings.TrimSpace(iss.Body)
	if body == "" {
		body = "_(issue sem corpo)_"
	}
	content := fmt.Sprintf(`# context.md — issue #%d (%s)

Semeado automaticamente pelo Argos com o corpo da issue (objetivo de negócio).
Decisões do humano e motivos de /reject são acrescentados abaixo.

## Corpo da issue

%s
`, task.Issue, task.Repo, body)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		log.Warn("runner: escrever context.md", "err", err)
		return
	}
	log.Info("runner: context.md semeado com o corpo da issue")
}

// specFileNames são os arquivos de spec por issue (spec §6).
var specFileNames = []string{"design.md", "tasks.md", "progress.md", "context.md"}

// existingSpecFiles retorna os caminhos absolutos dos arquivos de spec que
// existem e não estão vazios (para anexar no Telegram).
func existingSpecFiles(specDir string) []string {
	var out []string
	for _, name := range specFileNames {
		p := filepath.Join(specDir, name)
		if fi, err := os.Stat(p); err == nil && fi.Size() > 0 {
			out = append(out, p)
		}
	}
	return out
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
