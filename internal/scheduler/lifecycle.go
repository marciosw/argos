package scheduler

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/marciomacedo/argos/internal/domain"
	"github.com/marciomacedo/argos/internal/github"
)

// processRepo lista issues elegíveis do repo e despacha conforme o label dominante.
func (s *Scheduler) processRepo(ctx context.Context, repo string) {
	issues, err := s.poller.ListByLabels(ctx, repo, []string{domain.LabelReady, domain.LabelTodo})
	if err != nil {
		slog.Error("scheduler: ListByLabels", "repo", repo, "err", err)
		return
	}

	for _, iss := range issues {
		if hasLabel(iss.Labels, domain.LabelReady) {
			s.handleReady(ctx, repo, iss)
		} else if hasLabel(iss.Labels, domain.LabelTodo) {
			s.handleTodo(ctx, repo, iss)
		}
	}
}

// handleReady processa issue com label agent:ready:
// registra no store → adquire lock → muda label para documentation →
// lança goroutine de documentação.
func (s *Scheduler) handleReady(ctx context.Context, repo string, iss github.Issue) {
	issueID, err := s.store.UpsertIssue(ctx, repo, iss.Number, iss.Title, iss.URL, domain.PhaseReady)
	if err != nil {
		slog.Error("scheduler: handleReady UpsertIssue", "repo", repo, "issue", iss.Number, "err", err)
		return
	}

	ok, err := s.store.AcquireLock(ctx, issueID, "scheduler", s.cfg.LockLease.Std())
	if err != nil {
		slog.Error("scheduler: handleReady AcquireLock", "repo", repo, "issue", iss.Number, "err", err)
		return
	}
	if !ok {
		slog.Debug("scheduler: handleReady lock já existe, skip", "repo", repo, "issue", iss.Number)
		return
	}

	if err := s.poller.TransitionLabel(ctx, repo, iss.Number, domain.LabelReady, domain.LabelDocumentation); err != nil {
		slog.Error("scheduler: handleReady TransitionLabel", "repo", repo, "issue", iss.Number, "err", err)
		if relErr := s.store.ReleaseLock(ctx, issueID); relErr != nil {
			slog.Warn("scheduler: handleReady ReleaseLock após erro", "err", relErr)
		}
		return
	}

	if err := s.store.SetPhase(ctx, issueID, domain.PhaseDocumentation); err != nil {
		slog.Error("scheduler: handleReady SetPhase", "repo", repo, "issue", iss.Number, "err", err)
		if relErr := s.store.ReleaseLock(ctx, issueID); relErr != nil {
			slog.Warn("scheduler: handleReady ReleaseLock após erro", "err", relErr)
		}
		return
	}

	// Adquire semáforo antes de lançar goroutine.
	s.sem <- struct{}{}

	task := domain.Task{
		Repo:          repo,
		Issue:         iss.Number,
		Phase:         domain.PhaseDocumentation,
		Model:         s.cfg.Model.Default,
		ContextWindow: s.cfg.Model.ContextWindow,
	}

	go func() {
		defer func() { <-s.sem }()
		defer func() {
			if relErr := s.store.ReleaseLock(ctx, issueID); relErr != nil {
				slog.Warn("scheduler: handleReady ReleaseLock", "repo", repo, "issue", iss.Number, "err", relErr)
			}
		}()

		runErr := s.runner.Run(ctx, task)
		if runErr == nil {
			if err := s.store.SetPhase(ctx, issueID, domain.PhaseAwaitingApproval); err != nil {
				slog.Error("scheduler: handleReady SetPhase awaiting_approval",
					"repo", repo, "issue", iss.Number, "err", err)
			} else {
				slog.Info("scheduler: documentação concluída, aguardando aprovação",
					"repo", repo, "issue", iss.Number)
			}
		} else {
			slog.Error("scheduler: handleReady runner falhou",
				"repo", repo, "issue", iss.Number, "err", runErr)
			if err := s.poller.SetLabel(ctx, repo, iss.Number, domain.LabelError); err != nil {
				slog.Error("scheduler: handleReady SetLabel error", "err", err)
			}
			if err := s.store.SetPhase(ctx, issueID, domain.PhaseError); err != nil {
				slog.Error("scheduler: handleReady SetPhase error", "err", err)
			}
			if err := s.store.SetLastError(ctx, issueID, runErr.Error()); err != nil {
				slog.Error("scheduler: handleReady SetLastError", "err", err)
			}
		}
	}()
}

// handleTodo processa issue com label todo:
// adquire lock → muda label para doing → lança goroutine de codificação.
func (s *Scheduler) handleTodo(ctx context.Context, repo string, iss github.Issue) {
	dbIssue, err := s.store.GetIssue(ctx, repo, iss.Number)
	if err != nil {
		// Issue não está no store ainda — UpsertIssue e retry no próximo tick.
		issueID, uErr := s.store.UpsertIssue(ctx, repo, iss.Number, iss.Title, iss.URL, domain.PhaseTodo)
		if uErr != nil {
			slog.Error("scheduler: handleTodo UpsertIssue", "repo", repo, "issue", iss.Number, "err", uErr)
			return
		}
		dbIssue.ID = issueID
		dbIssue.Repo = repo
		dbIssue.Number = iss.Number
	}

	ok, err := s.store.AcquireLock(ctx, dbIssue.ID, "scheduler", s.cfg.LockLease.Std())
	if err != nil {
		slog.Error("scheduler: handleTodo AcquireLock", "repo", repo, "issue", iss.Number, "err", err)
		return
	}
	if !ok {
		slog.Debug("scheduler: handleTodo lock já existe, skip", "repo", repo, "issue", iss.Number)
		return
	}

	if err := s.poller.TransitionLabel(ctx, repo, iss.Number, domain.LabelTodo, domain.LabelDoing); err != nil {
		slog.Error("scheduler: handleTodo TransitionLabel", "repo", repo, "issue", iss.Number, "err", err)
		if relErr := s.store.ReleaseLock(ctx, dbIssue.ID); relErr != nil {
			slog.Warn("scheduler: handleTodo ReleaseLock após erro", "err", relErr)
		}
		return
	}

	if err := s.store.SetPhase(ctx, dbIssue.ID, domain.PhaseDoing); err != nil {
		slog.Error("scheduler: handleTodo SetPhase doing", "repo", repo, "issue", iss.Number, "err", err)
		if relErr := s.store.ReleaseLock(ctx, dbIssue.ID); relErr != nil {
			slog.Warn("scheduler: handleTodo ReleaseLock após erro", "err", relErr)
		}
		return
	}

	// Adquire semáforo antes de lançar goroutine.
	s.sem <- struct{}{}

	issueID := dbIssue.ID
	task := domain.Task{
		Repo:          repo,
		Issue:         iss.Number,
		Phase:         domain.PhaseDoing,
		Model:         s.cfg.Model.Default,
		ContextWindow: s.cfg.Model.ContextWindow,
	}

	go func() {
		defer func() { <-s.sem }()
		defer func() {
			if relErr := s.store.ReleaseLock(ctx, issueID); relErr != nil {
				slog.Warn("scheduler: handleTodo ReleaseLock", "repo", repo, "issue", iss.Number, "err", relErr)
			}
		}()

		runErr := s.runner.Run(ctx, task)
		if runErr == nil {
			if err := s.poller.TransitionLabel(ctx, repo, iss.Number, domain.LabelDoing, domain.LabelDone); err != nil {
				slog.Error("scheduler: handleTodo TransitionLabel done", "repo", repo, "issue", iss.Number, "err", err)
			}
			if err := s.store.SetPhase(ctx, issueID, domain.PhaseDone); err != nil {
				slog.Error("scheduler: handleTodo SetPhase done", "repo", repo, "issue", iss.Number, "err", err)
			} else {
				slog.Info("scheduler: codificação concluída", "repo", repo, "issue", iss.Number)
			}
		} else {
			slog.Error("scheduler: handleTodo runner falhou",
				"repo", repo, "issue", iss.Number, "err", runErr)
			if err := s.poller.SetLabel(ctx, repo, iss.Number, domain.LabelError); err != nil {
				slog.Error("scheduler: handleTodo SetLabel error", "err", err)
			}
			if err := s.store.SetPhase(ctx, issueID, domain.PhaseError); err != nil {
				slog.Error("scheduler: handleTodo SetPhase error", "err", err)
			}
			if err := s.store.SetLastError(ctx, issueID, fmt.Sprintf("%v", runErr)); err != nil {
				slog.Error("scheduler: handleTodo SetLastError", "err", err)
			}
		}
	}()
}

func hasLabel(labels []string, target string) bool {
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}
