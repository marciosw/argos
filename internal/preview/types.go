package preview

import (
	"context"
	"time"
)

// DevServerConfig configura o comportamento do DevServer por repo.
type DevServerConfig struct {
	// GoBackendEnabled controla se o servidor Go é iniciado para o repo redeagenda.
	// Default false — o frontend Flutter conecta direto ao Firebase em preview.
	GoBackendEnabled bool

	// GoBackendCmd é o binário do backend Go (ex.: "./server").
	// Recebe --port <extraPort> como argumento adicional.
	// Usado apenas quando GoBackendEnabled=true.
	GoBackendCmd string
}

// DefaultDevServerConfig retorna a config padrão do DevServer.
func DefaultDevServerConfig() DevServerConfig {
	return DevServerConfig{
		GoBackendEnabled: false,
		GoBackendCmd:     "./server",
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
