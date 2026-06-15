package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/marciomacedo/argos/internal/domain"
)

// Env vars reconhecidas pelo ConfigManager (model_config.md §4.1).
const (
	EnvModel            = "ORCHESTRATOR_MODEL"             // override global do modelo
	EnvModelPrefix      = "ORCHESTRATOR_MODEL_"            // + REPO (WEB/MOBILE/HYBRID)
	EnvContextThreshold = "ORCHESTRATOR_CONTEXT_THRESHOLD" // limiar de uso (0..1)
)

// ConfigManager resolve o ModelConfig efetivo aplicando a precedência
// issue > repo > env > arquivo > default (ver model_config.md §3). Concretiza
// a interface descrita no spec; Resolve nunca falha.
type ConfigManager struct {
	path string
	log  *slog.Logger

	mu  sync.RWMutex
	cfg *Config
}

// NewConfigManager cria um manager a partir de um Config já carregado. `path`
// é guardado para Reload (pode ser "" se a config não veio de arquivo).
func NewConfigManager(cfg *Config, path string, logger *slog.Logger) *ConfigManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &ConfigManager{path: path, log: logger, cfg: cfg}
}

// Config devolve o snapshot corrente da configuração.
func (m *ConfigManager) Config() *Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

// Reload relê o arquivo de config (hot reload). Sem path, é no-op.
func (m *ConfigManager) Reload() error {
	if m.path == "" {
		return nil
	}
	cfg, err := Load(m.path)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.cfg = cfg
	m.mu.Unlock()
	m.log.Info("config reloaded", "path", m.path)
	return nil
}

// Resolve calcula o ModelConfig efetivo para um repo+issue. Nunca falha: se
// nada estiver definido, retorna os defaults embutidos.
//
// Precedência do modelo (primeiro não-vazio vence):
//  1. override por issue   (arquivo: model.issues["repo#n"])
//  2. env por repo         (ORCHESTRATOR_MODEL_<REPO>)
//  3. override por repo    (arquivo: model.repos[repo])
//  4. env global           (ORCHESTRATOR_MODEL)
//  5. arquivo              (model.default)
//  6. default embutido     (DefaultModel)
func (m *ConfigManager) Resolve(repo string, issue int) domain.ModelConfig {
	m.mu.RLock()
	cfg := m.cfg
	m.mu.RUnlock()

	model := m.resolveModel(cfg, repo, issue)
	return domain.ModelConfig{
		Model:            model,
		ContextWindow:    m.resolveWindow(cfg, model),
		ContextThreshold: m.resolveThreshold(cfg),
	}
}

func (m *ConfigManager) resolveModel(cfg *Config, repo string, issue int) string {
	// 1. override por issue (arquivo)
	if issue > 0 {
		key := fmt.Sprintf("%s#%d", repo, issue)
		if ov, ok := cfg.Model.Issues[key]; ok && ov.Model != "" {
			return ov.Model
		}
	}
	// 2. env por repo
	if repo != "" {
		if v := os.Getenv(EnvModelPrefix + strings.ToUpper(repo)); v != "" {
			return v
		}
	}
	// 3. override por repo (arquivo)
	if ov, ok := cfg.Model.Repos[repo]; ok && ov.Model != "" {
		return ov.Model
	}
	// 4. env global
	if v := os.Getenv(EnvModel); v != "" {
		return v
	}
	// 5. arquivo (default) / 6. default embutido (já garantido por applyDefaults)
	if cfg.Model.Default != "" {
		return cfg.Model.Default
	}
	return DefaultModel
}

// resolveWindow busca a janela do modelo no mapa `windows`; se ausente, cai no
// model.context_window e loga um aviso (model_config.md §5).
func (m *ConfigManager) resolveWindow(cfg *Config, model string) int {
	if w, ok := cfg.Model.Windows[model]; ok && w > 0 {
		return w
	}
	fallback := cfg.Model.ContextWindow
	if fallback <= 0 {
		fallback = DefaultContextWindow
	}
	m.log.Warn("janela de contexto não declarada para o modelo; usando fallback",
		"model", model, "fallback", fallback)
	return fallback
}

func (m *ConfigManager) resolveThreshold(cfg *Config) float64 {
	if v := os.Getenv(EnvContextThreshold); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
		m.log.Warn("valor inválido em "+EnvContextThreshold+"; ignorado", "value", v)
	}
	if cfg.Model.ContextThreshold > 0 {
		return cfg.Model.ContextThreshold
	}
	return DefaultContextThreshold
}
