package preview

import (
	"context"
	"time"
)

// DevServerConfig configura o comportamento do DevServer por repo.
type DevServerConfig struct {
	// WebBackendEnabled controla se o uvicorn é iniciado para o repo web.
	// Default true (assume que vite.config.js proxia para o backend Python).
	// Setar false para repos SPA puro (sem proxy para backend).
	// Ver docs/specs/sprints/preview-v1.1/context.md §14.1.
	WebBackendEnabled bool

	// UvicornApp é o argumento app:factory passado ao uvicorn (ex.: "main:app").
	// Usado apenas quando WebBackendEnabled=true.
	UvicornApp string
}

// DefaultDevServerConfig retorna a config padrão do DevServer.
func DefaultDevServerConfig() DevServerConfig {
	return DevServerConfig{
		WebBackendEnabled: true,
		UvicornApp:        "main:app",
	}
}

// activePreview mantém o estado em memória de um preview em execução.
// Toda leitura/escrita deve ser protegida pelo mu do PreviewManager.
type activePreview struct {
	id        int64
	issueID   int64
	repo      string
	port      int
	extraPort int
	cancel    context.CancelFunc
	timer     *time.Timer
	expiresAt time.Time
}

// PreviewInfo é a visão externa de um preview para o comando /preview status.
type PreviewInfo struct {
	IssueID   int64
	Repo      string
	TunnelURL string
	ExpiresAt time.Time
	TimeLeft  time.Duration
}
