package preview

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"syscall"
	"time"

	"github.com/marciomacedo/argos/internal/config"
	"github.com/marciomacedo/argos/internal/domain"
)

// DevServer lança e monitora os processos de servidor de dev por tipo de repo.
type DevServer struct {
	cfg    config.PreviewConfig
	devCfg DevServerConfig
}

// NewDevServer cria um DevServer com a configuração fornecida.
func NewDevServer(cfg config.PreviewConfig, devCfg DevServerConfig) *DevServer {
	return &DevServer{cfg: cfg, devCfg: devCfg}
}

// Start lança o(s) processo(s) de dev para o repo e bloqueia até o servidor
// estar respondendo (healthcheck). ctx deve ser cancelável — ao cancelar, os
// processos são encerrados via SIGTERM (o grupo de processo inteiro).
func (ds *DevServer) Start(ctx context.Context, repo, checkoutDir string, port, extraPort int) error {
	log := slog.With("repo", repo, "port", port)
	switch repo {
	case domain.RepoRedeAgenda:
		return ds.startRedeAgenda(ctx, checkoutDir, port, extraPort, log)
	default:
		return fmt.Errorf("devserver: repo desconhecido: %q", repo)
	}
}

// startRedeAgenda sobe o servidor de preview do redeagenda: flutter web-server
// no port principal e, opcionalmente, o backend Go no extraPort.
func (ds *DevServer) startRedeAgenda(ctx context.Context, dir string, port, extraPort int, log *slog.Logger) error {
	if ds.devCfg.GoBackendEnabled && extraPort != 0 {
		log.Info("iniciando backend Go", "extra_port", extraPort, "cmd", ds.devCfg.GoBackendCmd)
		if err := ds.launch(ctx, dir, "go-backend", []string{
			ds.devCfg.GoBackendCmd,
			"--port", fmt.Sprint(extraPort),
		}, log); err != nil {
			return fmt.Errorf("devserver: go backend: %w", err)
		}
	}
	log.Info("iniciando flutter web-server", "port", port)
	if err := ds.launch(ctx, dir, "flutter-frontend", []string{
		"flutter", "run",
		"-d", "web-server",
		"--web-port", fmt.Sprint(port),
	}, log); err != nil {
		return fmt.Errorf("devserver: flutter: %w", err)
	}
	return ds.waitReady(ctx, port, log)
}

// launch inicia um subprocesso em background cujo tempo de vida é limitado ao ctx.
// Stdout/stderr são enviados para slog. O processo filho é criado em novo grupo
// de processos para que SIGTERM alcance toda a árvore quando o ctx for cancelado.
func (ds *DevServer) launch(ctx context.Context, dir, label string, args []string, log *slog.Logger) error {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("devserver: stdout pipe %s: %w", label, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("devserver: stderr pipe %s: %w", label, err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("devserver: iniciar %s: %w", label, err)
	}

	go pipeLines(ctx, stdout, label, "stdout", log)
	go pipeLines(ctx, stderr, label, "stderr", log)
	go func() { _ = cmd.Wait() }()

	return nil
}

// pipeLines lê r linha a linha e emite para slog enquanto ctx não for cancelado.
func pipeLines(ctx context.Context, r io.Reader, label, stream string, log *slog.Logger) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		log.Info(sc.Text(), "process", label, "stream", stream)
	}
}

// waitReady tenta GET http://localhost:port a cada 500ms até o servidor responder
// ou o timeout (StartupTimeoutSeconds) expirar.
func (ds *DevServer) waitReady(ctx context.Context, port int, log *slog.Logger) error {
	timeout := time.Duration(ds.cfg.StartupTimeoutSeconds) * time.Second
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("http://localhost:%d", port)
	client := &http.Client{Timeout: 2 * time.Second}

	log.Info("aguardando servidor ficar pronto", "url", url, "timeout_s", ds.cfg.StartupTimeoutSeconds)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			log.Info("servidor pronto", "url", url)
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("devserver: timeout após %ds aguardando %s", ds.cfg.StartupTimeoutSeconds, url)
}
