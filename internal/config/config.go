// Package config carrega a configuração do orquestrador (arquivo YAML + env) e
// resolve o modelo efetivo por repo/issue (ver model_config.md).
//
// Segredos (tokens) NUNCA ficam no arquivo: o YAML guarda apenas o NOME da env
// var que contém cada segredo (ex.: GITHUB_TOKEN), e o valor é lido do ambiente
// em runtime.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration é um time.Duration que (des)serializa de/para string no YAML
// (ex.: "60s", "5m").
type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("config: duração inválida %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) Std() time.Duration { return time.Duration(d) }

// Config é a configuração raiz (espelha config.example.yaml).
type Config struct {
	DBPath             string          `yaml:"db_path"`
	PollInterval       Duration        `yaml:"poll_interval"`
	MaxConcurrentTasks int             `yaml:"max_concurrent_tasks"`
	BaseBranch         string          `yaml:"base_branch"`
	AgentBranchPrefix  string          `yaml:"agent_branch_prefix"`
	LockLease          Duration        `yaml:"lock_lease"`
	LogLevel           string          `yaml:"log_level"`  // debug|info|warn|error
	LogFormat          string          `yaml:"log_format"` // text|json
	Workspace          WorkspaceConfig `yaml:"workspace"`
	GitHub             GitHubConfig    `yaml:"github"`
	Telegram           TelegramConfig  `yaml:"telegram"`
	Model              ModelSection    `yaml:"model"`
	Preview            PreviewConfig   `yaml:"preview"`
}

// WorkspaceConfig configura os checkouts locais dos repos-alvo no disco
// persistente da VM (design.md §12).
type WorkspaceConfig struct {
	// BaseDir é o diretório-raiz onde cada repo é clonado (subpasta por repo
	// lógico: <base_dir>/<repo>).
	BaseDir string `yaml:"base_dir"`
	// GitHost é o host de clone (default "github.com").
	GitHost string `yaml:"git_host"`
	// CloneScheme é o esquema de clone: "https" (token via env, default) — o
	// esquema "ssh" não é suportado nesta versão.
	CloneScheme string `yaml:"clone_scheme"`
}

// GitHubConfig configura o cliente GitHub (token via env).
type GitHubConfig struct {
	APIBase  string                `yaml:"api_base"`
	TokenEnv string                `yaml:"token_env"`
	Repos    map[string]RepoTarget `yaml:"repos"`
}

// RepoTarget identifica um repositório no GitHub.
type RepoTarget struct {
	Owner string `yaml:"owner"`
	Name  string `yaml:"name"`
}

// TelegramConfig configura o gateway do Telegram (tokens via env).
type TelegramConfig struct {
	BotTokenEnv    string  `yaml:"bot_token_env"`
	WebhookURL     string  `yaml:"webhook_url"`
	SecretTokenEnv string  `yaml:"secret_token_env"`
	AllowedChatIDs []int64 `yaml:"allowed_chat_ids"`
	MessageFormat  string  `yaml:"message_format"` // "html" | "markdownv2"
}

// ModelSection é o bloco de configuração de modelo (model_config.md §4.2).
type ModelSection struct {
	Default          string                   `yaml:"default"`
	ContextWindow    int                      `yaml:"context_window"`
	ContextThreshold float64                  `yaml:"context_threshold"`
	Windows          map[string]int           `yaml:"windows"`
	Repos            map[string]ModelOverride `yaml:"repos"`
	Issues           map[string]ModelOverride `yaml:"issues"` // chave "repo#n"
}

// ModelOverride é um override de modelo por repo ou por issue.
type ModelOverride struct {
	Model string `yaml:"model"`
}

// PreviewConfig configura o subsistema de preview de aplicações (/preview).
// Ver docs/specs/sprints/preview-v1.1/design.md §10.
type PreviewConfig struct {
	Enabled               bool   `yaml:"enabled"`
	PortRangeStart        int    `yaml:"port_range_start"`
	PortRangeEnd          int    `yaml:"port_range_end"`
	TimeoutMinutes        int    `yaml:"timeout_minutes"`
	StartupTimeoutSeconds int    `yaml:"startup_timeout_seconds"`
	CloudflaredBin        string `yaml:"cloudflared_bin"`
}

// Defaults de preview.
const (
	DefaultPreviewEnabled               = true
	DefaultPreviewPortRangeStart        = 9000
	DefaultPreviewPortRangeEnd          = 9099
	DefaultPreviewTimeoutMinutes        = 30
	DefaultPreviewStartupTimeoutSeconds = 30
	DefaultPreviewCloudflaredBin        = "cloudflared"
)

// Defaults embutidos (usados quando nada é configurado). Ver model_config.md §2.
const (
	DefaultModel            = "claude-opus-4-5" // opusplan
	DefaultContextWindow    = 200000
	DefaultContextThreshold = 0.65
	DefaultDBPath           = "./orchestrator.db"
	DefaultPollInterval     = 60 * time.Second
	DefaultLockLease        = 30 * time.Minute
	DefaultBaseBranch       = "main"
	DefaultBranchPrefix     = "agent/issue-"
	DefaultMessageFormat    = "html"
	DefaultLogLevel         = "info"
	DefaultLogFormat        = "text"
	DefaultWorkspaceBaseDir = "./checkouts"
	DefaultGitHost          = "github.com"
	DefaultCloneScheme      = "https"
)

// Load lê e parseia o arquivo de config e aplica os defaults faltantes. Um path
// vazio retorna a config só com defaults (sem arquivo).
func Load(path string) (*Config, error) {
	cfg := &Config{}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("config: ler %q: %w", path, err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("config: parse %q: %w", path, err)
		}
	}
	cfg.applyDefaults()
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.DBPath == "" {
		c.DBPath = DefaultDBPath
	}
	if c.PollInterval == 0 {
		c.PollInterval = Duration(DefaultPollInterval)
	}
	if c.MaxConcurrentTasks <= 0 {
		c.MaxConcurrentTasks = 1
	}
	if c.BaseBranch == "" {
		c.BaseBranch = DefaultBaseBranch
	}
	if c.AgentBranchPrefix == "" {
		c.AgentBranchPrefix = DefaultBranchPrefix
	}
	if c.LockLease == 0 {
		c.LockLease = Duration(DefaultLockLease)
	}
	if c.LogLevel == "" {
		c.LogLevel = DefaultLogLevel
	}
	if c.LogFormat == "" {
		c.LogFormat = DefaultLogFormat
	}
	if c.Workspace.BaseDir == "" {
		c.Workspace.BaseDir = DefaultWorkspaceBaseDir
	}
	if c.Workspace.GitHost == "" {
		c.Workspace.GitHost = DefaultGitHost
	}
	if c.Workspace.CloneScheme == "" {
		c.Workspace.CloneScheme = DefaultCloneScheme
	}
	if c.GitHub.APIBase == "" {
		c.GitHub.APIBase = "https://api.github.com"
	}
	if c.GitHub.TokenEnv == "" {
		c.GitHub.TokenEnv = "GITHUB_TOKEN"
	}
	if c.Telegram.BotTokenEnv == "" {
		c.Telegram.BotTokenEnv = "TELEGRAM_BOT_TOKEN"
	}
	if c.Telegram.SecretTokenEnv == "" {
		c.Telegram.SecretTokenEnv = "TELEGRAM_WEBHOOK_SECRET"
	}
	if c.Telegram.MessageFormat == "" {
		c.Telegram.MessageFormat = DefaultMessageFormat
	}
	if c.Model.Default == "" {
		c.Model.Default = DefaultModel
	}
	if c.Model.ContextWindow == 0 {
		c.Model.ContextWindow = DefaultContextWindow
	}
	if c.Model.ContextThreshold == 0 {
		c.Model.ContextThreshold = DefaultContextThreshold
	}
	if !c.Preview.Enabled && c.Preview.PortRangeStart == 0 {
		c.Preview.Enabled = DefaultPreviewEnabled
	}
	if c.Preview.PortRangeStart == 0 {
		c.Preview.PortRangeStart = DefaultPreviewPortRangeStart
	}
	if c.Preview.PortRangeEnd == 0 {
		c.Preview.PortRangeEnd = DefaultPreviewPortRangeEnd
	}
	if c.Preview.TimeoutMinutes == 0 {
		c.Preview.TimeoutMinutes = DefaultPreviewTimeoutMinutes
	}
	if c.Preview.StartupTimeoutSeconds == 0 {
		c.Preview.StartupTimeoutSeconds = DefaultPreviewStartupTimeoutSeconds
	}
	if c.Preview.CloudflaredBin == "" {
		c.Preview.CloudflaredBin = DefaultPreviewCloudflaredBin
	}
}
