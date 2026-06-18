package preview

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"syscall"
	"time"

	"github.com/marciomacedo/argos/internal/config"
)

var tunnelURLRe = regexp.MustCompile(`https://[a-z0-9\-]+\.trycloudflare\.com`)

// Tunnel gerencia o processo cloudflared que expõe um servidor local via HTTPS.
type Tunnel struct {
	cfg config.PreviewConfig
}

// NewTunnel cria um Tunnel com a configuração fornecida.
func NewTunnel(cfg config.PreviewConfig) *Tunnel {
	return &Tunnel{cfg: cfg}
}

// Start lança `cloudflared tunnel --url http://localhost:<port>`, lê o stderr
// até encontrar a URL pública e a retorna. Se o processo morrer depois que Start
// retornar, onCrash é chamado (pode ser nil).
//
// ctx deve ser cancelável — ao cancelar, o processo cloudflared é encerrado.
func (t *Tunnel) Start(ctx context.Context, port int, onCrash func()) (string, error) {
	bin := t.cfg.CloudflaredBin
	if bin == "" {
		bin = "cloudflared"
	}
	log := slog.With("component", "cloudflared", "port", port)

	cmd := exec.CommandContext(ctx, bin, "tunnel", "--url",
		fmt.Sprintf("http://localhost:%d", port))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("tunnel: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("tunnel: iniciar cloudflared: %w", err)
	}
	log.Info("cloudflared iniciado")

	urlCh := make(chan string, 1)
	scanErr := make(chan error, 1)

	go func() {
		sc := bufio.NewScanner(stderr)
		found := false
		for sc.Scan() {
			line := sc.Text()
			log.Info(line)
			if !found {
				if m := tunnelURLRe.FindString(line); m != "" {
					found = true
					urlCh <- m
				}
			}
		}
		if !found {
			scanErr <- fmt.Errorf("tunnel: cloudflared encerrou sem fornecer URL")
		}
	}()

	timeout := time.Duration(t.cfg.StartupTimeoutSeconds) * time.Second
	select {
	case url := <-urlCh:
		log.Info("URL do túnel obtida", "url", url)
		go t.monitor(ctx, cmd, onCrash, log)
		return url, nil
	case err := <-scanErr:
		_ = cmd.Process.Kill()
		return "", err
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("tunnel: timeout após %ds aguardando URL do cloudflared",
			t.cfg.StartupTimeoutSeconds)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// monitor aguarda o processo terminar e chama onCrash se a queda não foi por
// cancelamento de ctx.
func (t *Tunnel) monitor(ctx context.Context, cmd *exec.Cmd, onCrash func(), log *slog.Logger) {
	_ = cmd.Wait()
	select {
	case <-ctx.Done():
		log.Info("cloudflared encerrado por cancelamento de contexto")
	default:
		log.Error("cloudflared caiu inesperadamente")
		if onCrash != nil {
			onCrash()
		}
	}
}
