// Package scheduler contém o loop central de orquestração do Argos: a cada
// tick de polling, reconcilia labels do GitHub com o estado SQLite e despacha
// tarefas elegíveis ao TaskRunner respeitando o semáforo de concorrência.
//
// Ver docs/specs/design.md §4, §6, §7, §8.
package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/marciomacedo/argos/internal/config"
	"github.com/marciomacedo/argos/internal/domain"
	"github.com/marciomacedo/argos/internal/github"
	"github.com/marciomacedo/argos/internal/store"
)

// Scheduler é o loop central de orquestração.
type Scheduler struct {
	store  *store.Store
	poller github.Poller
	runner TaskRunner
	cfg    *config.Config
	cmds   <-chan domain.Command
	// sem é o semáforo de concorrência: capacidade = cfg.MaxConcurrentTasks.
	// Adquirido no caller antes de lançar goroutine; liberado dentro da goroutine.
	sem chan struct{}
}

// New cria um Scheduler com o semáforo dimensionado por cfg.MaxConcurrentTasks.
func New(
	st *store.Store,
	poller github.Poller,
	runner TaskRunner,
	cfg *config.Config,
	cmds <-chan domain.Command,
) *Scheduler {
	cap := cfg.MaxConcurrentTasks
	if cap <= 0 {
		cap = 1
	}
	return &Scheduler{
		store:  st,
		poller: poller,
		runner: runner,
		cfg:    cfg,
		cmds:   cmds,
		sem:    make(chan struct{}, cap),
	}
}

// Run inicializa os repos configurados e entra no loop principal.
// Retorna quando ctx é cancelado.
func (s *Scheduler) Run(ctx context.Context) {
	for repo := range s.cfg.GitHub.Repos {
		if err := s.store.EnsureRepo(ctx, repo); err != nil {
			slog.Error("scheduler: EnsureRepo falhou", "repo", repo, "err", err)
		}
	}

	ticker := time.NewTicker(s.cfg.PollInterval.Std())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case cmd, ok := <-s.cmds:
			if ok {
				s.handleCommand(ctx, cmd)
			}
		case <-ticker.C:
			s.processTick(ctx)
		}
	}
}

// processTick é executado a cada tick: reap de locks expirados e processamento
// de cada repo ativo.
func (s *Scheduler) processTick(ctx context.Context) {
	if n, err := s.store.ReapExpiredLocks(ctx, time.Now()); err != nil {
		slog.Warn("scheduler: ReapExpiredLocks", "err", err)
	} else if n > 0 {
		slog.Info("scheduler: locks expirados removidos", "count", n)
	}

	repos, err := s.store.ListActiveRepos(ctx)
	if err != nil {
		slog.Error("scheduler: ListActiveRepos", "err", err)
		return
	}

	for _, repo := range repos {
		s.processRepo(ctx, repo)
	}
}

// handleCommand processa um comando do Telegram.
func (s *Scheduler) handleCommand(ctx context.Context, cmd domain.Command) {
	switch cmd.Type {
	case domain.CmdApprove:
		s.cmdApprove(ctx, cmd)
	case domain.CmdReject:
		s.cmdReject(ctx, cmd)
	case domain.CmdPause:
		if err := s.store.SetRepoPaused(ctx, cmd.Repo, true, "pausado via Telegram"); err != nil {
			slog.Error("scheduler: CmdPause", "repo", cmd.Repo, "err", err)
			return
		}
		slog.Info("scheduler: repo pausado", "repo", cmd.Repo)
		if err := s.store.Audit(ctx, 0, cmd.Repo, "repo_paused", "{}"); err != nil {
			slog.Warn("scheduler: audit CmdPause", "err", err)
		}
	case domain.CmdResume:
		if err := s.store.SetRepoPaused(ctx, cmd.Repo, false, ""); err != nil {
			slog.Error("scheduler: CmdResume", "repo", cmd.Repo, "err", err)
			return
		}
		slog.Info("scheduler: repo retomado", "repo", cmd.Repo)
		if err := s.store.Audit(ctx, 0, cmd.Repo, "repo_resumed", "{}"); err != nil {
			slog.Warn("scheduler: audit CmdResume", "err", err)
		}
	case domain.CmdStatus, domain.CmdRun:
		slog.Info("scheduler: comando não implementado nesta sessão", "type", cmd.Type)
	}
}

func (s *Scheduler) cmdApprove(ctx context.Context, cmd domain.Command) {
	issue, repo, found := s.findIssue(ctx, cmd.Issue)
	if !found {
		slog.Warn("scheduler: CmdApprove — issue não encontrada", "issue", cmd.Issue)
		return
	}
	if issue.Phase != domain.PhaseAwaitingApproval {
		slog.Warn("scheduler: CmdApprove — issue não está em awaiting_approval",
			"repo", repo, "issue", cmd.Issue, "phase", issue.Phase)
		return
	}
	if err := s.store.SetPhase(ctx, issue.ID, domain.PhaseTodo); err != nil {
		slog.Error("scheduler: CmdApprove SetPhase", "repo", repo, "issue", cmd.Issue, "err", err)
		return
	}
	if err := s.poller.TransitionLabel(ctx, repo, cmd.Issue, domain.LabelDocumentation, domain.LabelTodo); err != nil {
		slog.Error("scheduler: CmdApprove TransitionLabel", "repo", repo, "issue", cmd.Issue, "err", err)
		return
	}
	if err := s.store.Audit(ctx, issue.ID, repo, "approved", "{}"); err != nil {
		slog.Warn("scheduler: audit approve", "err", err)
	}
	slog.Info("scheduler: issue aprovada", "repo", repo, "issue", cmd.Issue)
}

func (s *Scheduler) cmdReject(ctx context.Context, cmd domain.Command) {
	issue, repo, found := s.findIssue(ctx, cmd.Issue)
	if !found {
		slog.Warn("scheduler: CmdReject — issue não encontrada", "issue", cmd.Issue)
		return
	}
	if err := s.store.SetPhase(ctx, issue.ID, domain.PhaseDocumentation); err != nil {
		slog.Error("scheduler: CmdReject SetPhase", "repo", repo, "issue", cmd.Issue, "err", err)
		return
	}
	body := "Rejeitado: " + cmd.Reason
	if err := s.poller.Comment(ctx, repo, cmd.Issue, body); err != nil {
		slog.Error("scheduler: CmdReject Comment", "repo", repo, "issue", cmd.Issue, "err", err)
	}
	detail := `{"reason":` + `"` + cmd.Reason + `"}`
	if err := s.store.Audit(ctx, issue.ID, repo, "rejected", detail); err != nil {
		slog.Warn("scheduler: audit reject", "err", err)
	}
	slog.Info("scheduler: issue rejeitada", "repo", repo, "issue", cmd.Issue, "reason", cmd.Reason)
}

// findIssue procura uma issue por número em todos os repos configurados.
// Retorna (issue, repo, true) se encontrada.
func (s *Scheduler) findIssue(ctx context.Context, issueNum int) (domain.Issue, string, bool) {
	for repo := range s.cfg.GitHub.Repos {
		issue, err := s.store.GetIssue(ctx, repo, issueNum)
		if err == nil {
			return issue, repo, true
		}
	}
	return domain.Issue{}, "", false
}
