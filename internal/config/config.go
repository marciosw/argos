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
	DBPath             string         `yaml:"db_path"`
	PollInterval       Duration       `yaml:"poll_interval"`
	MaxConcurrentTasks int            `yaml:"max_concurrent_tasks"`
	BaseBranch         string         `yaml:"base_branch"`
	AgentBranchPrefix  string         `yaml:"agent_branch_prefix"`
	LockLease          Duration       `yaml:"lock_lease"`
	GitHub             GitHubConfig   `yaml:"github"`
	Telegram           TelegramConfig `yaml:"telegram"`
	Model              ModelSection   `yaml:"model"`
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
}
