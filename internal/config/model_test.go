package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/marciomacedo/argos/internal/domain"
)

func newManager(t *testing.T, cfg *Config) *ConfigManager {
	t.Helper()
	cfg.applyDefaults()
	return NewConfigManager(cfg, "", nil)
}

func TestResolveBuiltinDefault(t *testing.T) {
	// Config vazia (só defaults) e sem env → default embutido.
	clearModelEnv(t)
	m := newManager(t, &Config{})
	mc := m.Resolve(domain.RepoWeb, 0)
	if mc.Model != DefaultModel {
		t.Fatalf("model = %q, quero %q", mc.Model, DefaultModel)
	}
	if mc.ContextWindow != DefaultContextWindow {
		t.Fatalf("window = %d, quero %d", mc.ContextWindow, DefaultContextWindow)
	}
	if mc.ContextThreshold != DefaultContextThreshold {
		t.Fatalf("threshold = %v, quero %v", mc.ContextThreshold, DefaultContextThreshold)
	}
}

func TestResolveFileDefault(t *testing.T) {
	clearModelEnv(t)
	m := newManager(t, &Config{
		Model: ModelSection{Default: "claude-sonnet-4-6"},
	})
	if got := m.Resolve(domain.RepoWeb, 0).Model; got != "claude-sonnet-4-6" {
		t.Fatalf("model = %q", got)
	}
}

func TestResolvePrecedence(t *testing.T) {
	clearModelEnv(t)
	cfg := &Config{
		Model: ModelSection{
			Default: "file-default",
			Repos: map[string]ModelOverride{
				domain.RepoWeb: {Model: "repo-web-model"},
			},
			Issues: map[string]ModelOverride{
				"web#42": {Model: "issue-42-model"},
			},
		},
	}
	m := newManager(t, cfg)

	// 1. Issue override vence tudo.
	if got := m.Resolve(domain.RepoWeb, 42).Model; got != "issue-42-model" {
		t.Fatalf("issue override: %q", got)
	}
	// 2. Sem issue override, vence o override por repo.
	if got := m.Resolve(domain.RepoWeb, 7).Model; got != "repo-web-model" {
		t.Fatalf("repo override: %q", got)
	}
	// 3. Repo sem override cai no default do arquivo.
	if got := m.Resolve(domain.RepoMobile, 0).Model; got != "file-default" {
		t.Fatalf("file default: %q", got)
	}
}

func TestResolveEnvPrecedence(t *testing.T) {
	clearModelEnv(t)
	cfg := &Config{
		Model: ModelSection{
			Default: "file-default",
			Repos: map[string]ModelOverride{
				domain.RepoWeb: {Model: "repo-web-model"},
			},
		},
	}
	m := newManager(t, cfg)

	// Env global vence o default do arquivo (mas não o override por repo).
	t.Setenv(EnvModel, "env-global")
	if got := m.Resolve(domain.RepoMobile, 0).Model; got != "env-global" {
		t.Fatalf("env global: %q", got)
	}
	if got := m.Resolve(domain.RepoWeb, 0).Model; got != "repo-web-model" {
		t.Fatalf("repo override deve vencer env global: %q", got)
	}

	// Env por repo vence o override de arquivo do mesmo repo e a issue manda mais.
	t.Setenv(EnvModelPrefix+"WEB", "env-web")
	if got := m.Resolve(domain.RepoWeb, 0).Model; got != "env-web" {
		t.Fatalf("env por repo: %q", got)
	}
}

func TestResolveIssueOverrideBeatsRepoEnv(t *testing.T) {
	clearModelEnv(t)
	cfg := &Config{
		Model: ModelSection{
			Issues: map[string]ModelOverride{"web#5": {Model: "issue-5"}},
		},
	}
	m := newManager(t, cfg)
	t.Setenv(EnvModelPrefix+"WEB", "env-web")
	if got := m.Resolve(domain.RepoWeb, 5).Model; got != "issue-5" {
		t.Fatalf("issue override deve vencer env por repo: %q", got)
	}
}

func TestResolveWindow(t *testing.T) {
	clearModelEnv(t)
	cfg := &Config{
		Model: ModelSection{
			Default:       "claude-opus-4-8",
			ContextWindow: 200000,
			Windows: map[string]int{
				"claude-opus-4-8": 1000000,
			},
		},
	}
	m := newManager(t, cfg)
	// Modelo na tabela windows → usa o valor declarado.
	if got := m.Resolve(domain.RepoWeb, 0).ContextWindow; got != 1000000 {
		t.Fatalf("window declarada: %d", got)
	}
	// Modelo fora da tabela → fallback para model.context_window.
	cfg.Model.Default = "modelo-desconhecido"
	if got := m.Resolve(domain.RepoWeb, 0).ContextWindow; got != 200000 {
		t.Fatalf("window fallback: %d", got)
	}
}

func TestResolveThresholdEnv(t *testing.T) {
	clearModelEnv(t)
	m := newManager(t, &Config{Model: ModelSection{ContextThreshold: 0.65}})

	// Sem env → valor do arquivo.
	if got := m.Resolve(domain.RepoWeb, 0).ContextThreshold; got != 0.65 {
		t.Fatalf("threshold arquivo: %v", got)
	}
	// Env válido sobrescreve.
	t.Setenv(EnvContextThreshold, "0.8")
	if got := m.Resolve(domain.RepoWeb, 0).ContextThreshold; got != 0.8 {
		t.Fatalf("threshold env: %v", got)
	}
	// Env inválido é ignorado (cai no arquivo).
	t.Setenv(EnvContextThreshold, "abc")
	if got := m.Resolve(domain.RepoWeb, 0).ContextThreshold; got != 0.65 {
		t.Fatalf("threshold env inválido: %v", got)
	}
}

func TestLoadAndReload(t *testing.T) {
	clearModelEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, "model:\n  default: \"first\"\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	m := NewConfigManager(cfg, path, nil)
	if got := m.Resolve(domain.RepoWeb, 0).Model; got != "first" {
		t.Fatalf("antes do reload: %q", got)
	}

	// Reescreve o arquivo e recarrega.
	writeFile(t, path, "model:\n  default: \"second\"\n")
	if err := m.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := m.Resolve(domain.RepoWeb, 0).Model; got != "second" {
		t.Fatalf("após reload: %q", got)
	}
}

func TestLoadDefaultsApplied(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load vazio: %v", err)
	}
	if cfg.DBPath != DefaultDBPath || cfg.MaxConcurrentTasks != 1 ||
		cfg.BaseBranch != DefaultBaseBranch || cfg.PollInterval.Std() != DefaultPollInterval {
		t.Fatalf("defaults não aplicados: %+v", cfg)
	}
}

// clearModelEnv garante que as env vars de modelo não vazem entre testes.
func clearModelEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{EnvModel, EnvContextThreshold,
		EnvModelPrefix + "WEB", EnvModelPrefix + "MOBILE", EnvModelPrefix + "HYBRID"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
