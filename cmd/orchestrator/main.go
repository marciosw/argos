// Command orchestrator é o bootstrap do Argos: um processo único, long-running
// (gerido por systemd na VM — design.md §7, §12) que sobe o store SQLite, o
// poller do GitHub, o gateway do Telegram (webhook), o workspace de checkouts e
// o scheduler de orquestração. Encerra de forma graciosa em SIGINT/SIGTERM.
//
// Segredos (tokens) são lidos do ambiente pelos componentes e NUNCA logados.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/marciomacedo/argos/internal/config"
	"github.com/marciomacedo/argos/internal/domain"
	"github.com/marciomacedo/argos/internal/github"
	"github.com/marciomacedo/argos/internal/preview"
	"github.com/marciomacedo/argos/internal/runner"
	"github.com/marciomacedo/argos/internal/scheduler"
	"github.com/marciomacedo/argos/internal/store"
	"github.com/marciomacedo/argos/internal/telegram"
	"github.com/marciomacedo/argos/internal/workspace"
)

// cmdBuffer é a capacidade do canal de comandos Telegram→Scheduler.
const cmdBuffer = 32

// shutdownTimeout limita a espera pelo encerramento das goroutines.
const shutdownTimeout = 30 * time.Second

func main() {
	configPath := flag.String("config", "", "caminho do config.yaml (vazio = só defaults)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		// Logger ainda não configurado; usa o default.
		slog.Error("config: falha ao carregar", "err", err)
		os.Exit(1)
	}

	setupLogging(cfg)
	slog.Info("argos: iniciando", "config", *configPath, "db", cfg.DBPath,
		"poll_interval", cfg.PollInterval.Std().String(), "max_concurrent", cfg.MaxConcurrentTasks)

	// Contexto cancelável por sinal (SIGINT/SIGTERM) → shutdown gracioso.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, store.Options{Path: cfg.DBPath})
	if err != nil {
		slog.Error("store: falha ao abrir", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	for repo := range cfg.GitHub.Repos {
		if err := st.EnsureRepo(ctx, repo); err != nil {
			slog.Error("store: EnsureRepo", "repo", repo, "err", err)
			os.Exit(1)
		}
	}

	poller, err := github.NewPollerFromConfig(cfg.GitHub)
	if err != nil {
		slog.Error("github: falha ao criar poller", "err", err)
		os.Exit(1)
	}

	cmds := make(chan domain.Command, cmdBuffer)

	gw, err := telegram.New(&cfg.Telegram, st, cmds)
	if err != nil {
		slog.Error("telegram: falha ao criar gateway", "err", err)
		os.Exit(1)
	}

	// Subsistema de preview (/preview). O gateway é construído antes para ser
	// injetado como preview.Notifier; em seguida o previewMgr é registrado de
	// volta no gateway (resolve a dependência circular). Ver design.md §12.
	if cfg.Preview.Enabled {
		portMgr := preview.NewPortManager(cfg.Preview)
		devCfg := preview.DefaultDevServerConfig()
		previewMgr := preview.NewPreviewManager(st, portMgr, gw, cfg.Preview, devCfg)
		if err := previewMgr.RecoverStale(ctx); err != nil {
			slog.Error("preview: RecoverStale falhou", "err", err) // não fatal
		}
		gw.RegisterPreviewManager(previewMgr, cfg.Workspace.BaseDir)
		slog.Info("preview: subsistema habilitado",
			"port_range", []int{cfg.Preview.PortRangeStart, cfg.Preview.PortRangeEnd},
			"timeout_min", cfg.Preview.TimeoutMinutes)
	}

	ws := workspace.New(workspace.Options{
		BaseDir:    cfg.Workspace.BaseDir,
		Repos:      cfg.GitHub.Repos,
		BaseBranch: cfg.BaseBranch,
		Host:       cfg.Workspace.GitHost,
		Token:      os.Getenv(cfg.GitHub.TokenEnv), // nunca logado
	})

	r := runner.New(cfg, st, poller, gw, ws)
	sch := scheduler.New(st, poller, r, cfg, cmds)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		if err := gw.Start(ctx); err != nil {
			slog.Error("telegram: gateway encerrou com erro", "err", err)
			stop() // derruba o resto se o webhook falhar ao subir
		}
	}()

	go func() {
		defer wg.Done()
		sch.Run(ctx)
	}()

	slog.Info("argos: em execução")

	<-ctx.Done()
	slog.Info("argos: sinal de encerramento recebido, drenando...")

	// Aguarda as goroutines (gateway faz srv.Shutdown; scheduler retorna) com
	// um teto de tempo para não travar o shutdown.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		slog.Info("argos: encerrado de forma graciosa")
	case <-time.After(shutdownTimeout):
		slog.Warn("argos: timeout de shutdown atingido, encerrando assim mesmo",
			"timeout", shutdownTimeout.String())
	}
}

// setupLogging configura o slog default a partir da config (nível + formato).
func setupLogging(cfg *config.Config) {
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if cfg.LogFormat == "json" {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(h))
}
