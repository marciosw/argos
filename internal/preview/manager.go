package preview

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/marciomacedo/argos/internal/config"
	"github.com/marciomacedo/argos/internal/domain"
)

// Store define as operações de persistência usadas pelo PreviewManager.
// Implementada por *store.Store.
type Store interface {
	CreatePreview(ctx context.Context, issueID int64, repo string, port, extraPort int) (int64, error)
	SetPreviewRunning(ctx context.Context, id int64, tunnelURL string) error
	StopPreview(ctx context.Context, id int64, status domain.PreviewStatus, reason domain.PreviewStopReason) error
	GetActivePreview(ctx context.Context) (domain.Preview, error)
	GetPreviewByIssue(ctx context.Context, issueID int64) (domain.Preview, error)
	MarkStalePreviewsDead(ctx context.Context) ([]int64, error)
	ListPreviews(ctx context.Context, statusFilter []domain.PreviewStatus) ([]domain.Preview, error)
}

// Notifier é a interface mínima de notificação usada pelo PreviewManager.
// Implementada por *telegram.Gateway.
type Notifier interface {
	NotifyPreviewReady(ctx context.Context, issueID int64, url string, expiresMin int) error
	NotifyPreviewStopped(ctx context.Context, issueID int64, reason domain.PreviewStopReason) error
	NotifyPreviewReplaced(ctx context.Context, oldIssueID, newIssueID int64) error
	NotifyPreviewError(ctx context.Context, issueID int64, err error) error
}

// PreviewManager orquestra o ciclo de vida completo de um preview:
// start / stop / status + timeout automático + recovery no startup.
// Apenas 1 preview ativo por vez (max_previews=1).
type PreviewManager struct {
	store    Store
	portMgr  *PortManager
	notifier Notifier
	devSrv   *DevServer
	tunnel   *Tunnel
	cfg      config.PreviewConfig

	mu     sync.Mutex
	active *activePreview
}

// NewPreviewManager cria um PreviewManager pronto para uso.
func NewPreviewManager(
	store Store,
	portMgr *PortManager,
	notifier Notifier,
	cfg config.PreviewConfig,
	devCfg DevServerConfig,
) *PreviewManager {
	return &PreviewManager{
		store:    store,
		portMgr:  portMgr,
		notifier: notifier,
		devSrv:   NewDevServer(cfg, devCfg),
		tunnel:   NewTunnel(cfg),
		cfg:      cfg,
	}
}

// Start inicia um preview para a issue. Se já houver um preview ativo, ele é
// derrubado antes de iniciar o novo (com notificação de substituição).
// checkoutDir é o caminho local do repo-alvo (gerenciado pelo workspace).
func (pm *PreviewManager) Start(ctx context.Context, issueID int64, repo, checkoutDir string) error {
	// Derrubar preview anterior, se existir.
	pm.mu.Lock()
	prev := pm.active
	pm.mu.Unlock()

	if prev != nil {
		_ = pm.notifier.NotifyPreviewReplaced(ctx, prev.issueID, issueID)
		pm.stopLocked(prev, domain.PreviewStopReplaced)
	}

	port, extraPort, err := pm.portMgr.Alloc(repo)
	if err != nil {
		_ = pm.notifier.NotifyPreviewError(ctx, issueID, err)
		return fmt.Errorf("preview: alocar portas: %w", err)
	}

	pid, err := pm.store.CreatePreview(ctx, issueID, repo, port, extraPort)
	if err != nil {
		pm.portMgr.Release(port, extraPort)
		_ = pm.notifier.NotifyPreviewError(ctx, issueID, err)
		return fmt.Errorf("preview: persistir starting: %w", err)
	}

	// Contexto do preview é independente do request — sobrevive ao HTTP handler.
	previewCtx, cancel := context.WithCancel(context.Background())

	ap := &activePreview{
		id:        pid,
		issueID:   issueID,
		repo:      repo,
		port:      port,
		extraPort: extraPort,
		cancel:    cancel,
		expiresAt: time.Now().Add(time.Duration(pm.cfg.TimeoutMinutes) * time.Minute),
	}

	pm.mu.Lock()
	pm.active = ap
	pm.mu.Unlock()

	if err := pm.devSrv.Start(previewCtx, repo, checkoutDir, port, extraPort); err != nil {
		pm.cleanupFailed(ctx, ap, domain.PreviewStopCrash)
		_ = pm.notifier.NotifyPreviewError(ctx, issueID, err)
		return fmt.Errorf("preview: devserver: %w", err)
	}

	url, err := pm.tunnel.Start(previewCtx, port, func() {
		slog.Error("cloudflared caiu inesperadamente", "issue_id", issueID, "port", port)
		if stopErr := pm.Stop(issueID, domain.PreviewStopCrash); stopErr == nil {
			_ = pm.notifier.NotifyPreviewStopped(context.Background(), issueID, domain.PreviewStopCrash)
		}
	})
	if err != nil {
		pm.cleanupFailed(ctx, ap, domain.PreviewStopCrash)
		_ = pm.notifier.NotifyPreviewError(ctx, issueID, err)
		return fmt.Errorf("preview: tunnel: %w", err)
	}

	if err := pm.store.SetPreviewRunning(ctx, pid, url); err != nil {
		pm.cleanupFailed(ctx, ap, domain.PreviewStopCrash)
		return fmt.Errorf("preview: atualizar running: %w", err)
	}

	_ = pm.notifier.NotifyPreviewReady(ctx, issueID, url, pm.cfg.TimeoutMinutes)

	ap.timer = time.AfterFunc(
		time.Duration(pm.cfg.TimeoutMinutes)*time.Minute,
		func() {
			if err := pm.Stop(issueID, domain.PreviewStopTimeout); err == nil {
				_ = pm.notifier.NotifyPreviewStopped(
					context.Background(), issueID, domain.PreviewStopTimeout)
			}
		},
	)

	slog.Info("preview iniciado", "issue_id", issueID, "repo", repo, "url", url,
		"expires_at", ap.expiresAt.Format(time.RFC3339))
	return nil
}

// Stop encerra o preview da issue com o motivo informado. Retorna erro se não
// há preview ativo para a issue.
func (pm *PreviewManager) Stop(issueID int64, reason domain.PreviewStopReason) error {
	pm.mu.Lock()
	ap := pm.active
	if ap == nil || ap.issueID != issueID {
		pm.mu.Unlock()
		return fmt.Errorf("preview: issue %d não tem preview ativo", issueID)
	}
	pm.active = nil
	pm.mu.Unlock()

	pm.stopLocked(ap, reason)
	slog.Info("preview encerrado", "issue_id", issueID, "reason", reason)
	return nil
}

// StatusAll retorna informações sobre o preview ativo (zero ou um elemento).
func (pm *PreviewManager) StatusAll(ctx context.Context) ([]PreviewInfo, error) {
	pm.mu.Lock()
	ap := pm.active
	pm.mu.Unlock()

	if ap == nil {
		return nil, nil
	}

	p, err := pm.store.GetActivePreview(ctx)
	if err != nil {
		return nil, fmt.Errorf("preview: status: %w", err)
	}

	now := time.Now()
	left := ap.expiresAt.Sub(now)
	if left < 0 {
		left = 0
	}

	return []PreviewInfo{{
		IssueID:   ap.issueID,
		Repo:      ap.repo,
		TunnelURL: p.TunnelURL,
		ExpiresAt: ap.expiresAt,
		TimeLeft:  left,
	}}, nil
}

// RecoverStale marca como dead os previews que ficaram em estado transitório
// (ex.: após restart do Argos) e envia notificação para cada um.
// Deve ser chamado no startup, antes do Scheduler iniciar.
func (pm *PreviewManager) RecoverStale(ctx context.Context) error {
	stale, err := pm.store.ListPreviews(ctx, []domain.PreviewStatus{
		domain.PreviewStarting,
		domain.PreviewRunning,
		domain.PreviewStopping,
	})
	if err != nil {
		return fmt.Errorf("preview: recover stale list: %w", err)
	}
	if len(stale) == 0 {
		return nil
	}

	if _, err := pm.store.MarkStalePreviewsDead(ctx); err != nil {
		return fmt.Errorf("preview: marcar stale dead: %w", err)
	}

	for _, p := range stale {
		slog.Warn("preview marcado como dead no restart",
			"preview_id", p.ID, "issue_id", p.IssueID, "repo", p.Repo)
		_ = pm.notifier.NotifyPreviewStopped(ctx, p.IssueID, domain.PreviewStopRestart)
	}
	return nil
}

// stopLocked cancela o contexto do preview, para o timer e atualiza o SQLite.
// Não requer pm.mu (o caller já removeu ap de pm.active se necessário).
func (pm *PreviewManager) stopLocked(ap *activePreview, reason domain.PreviewStopReason) {
	if ap.timer != nil {
		ap.timer.Stop()
	}
	ap.cancel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pm.store.StopPreview(ctx, ap.id, domain.PreviewStopped, reason); err != nil {
		slog.Error("falha ao persistir stop do preview", "preview_id", ap.id, "err", err)
	}
	pm.portMgr.Release(ap.port, ap.extraPort)
}

// cleanupFailed remove um preview que falhou no startup (antes de estar running).
func (pm *PreviewManager) cleanupFailed(ctx context.Context, ap *activePreview, reason domain.PreviewStopReason) {
	ap.cancel()

	pm.mu.Lock()
	if pm.active == ap {
		pm.active = nil
	}
	pm.mu.Unlock()

	_ = pm.store.StopPreview(ctx, ap.id, domain.PreviewDead, reason)
	pm.portMgr.Release(ap.port, ap.extraPort)
}
