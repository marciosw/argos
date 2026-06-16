package scheduler

import (
	"context"

	"github.com/marciomacedo/argos/internal/domain"
)

// TaskRunner executa uma tarefa de codificação/documentação para uma issue.
// A implementação real vive em internal/runner/ (Sessão 4+).
type TaskRunner interface {
	Run(ctx context.Context, task domain.Task) error
}
