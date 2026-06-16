package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/marciomacedo/argos/internal/config"
	"github.com/marciomacedo/argos/internal/domain"
	"github.com/marciomacedo/argos/internal/store"
)

// tgClient é o cliente HTTP mínimo para a Telegram Bot API.
// O token nunca é logado.
type tgClient struct {
	base     string // ex.: "https://api.telegram.org"
	botToken string
	http     *http.Client
}

func (c *tgClient) apiURL(method string) string {
	return fmt.Sprintf("%s/bot%s/%s", c.base, c.botToken, method)
}

func (c *tgClient) setWebhook(ctx context.Context, webhookURL, secretToken string) error {
	body, _ := json.Marshal(map[string]string{
		"url":          webhookURL,
		"secret_token": secretToken,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL("setWebhook"), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram: setWebhook: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.doExpectOK(req, "setWebhook")
}

func (c *tgClient) sendMessage(ctx context.Context, chatID int64, text, parseMode string) error {
	body, _ := json.Marshal(map[string]any{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": parseMode,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL("sendMessage"), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram: sendMessage: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.doExpectOK(req, "sendMessage")
}

func (c *tgClient) sendDocument(ctx context.Context, chatID int64, filename string, content []byte, caption string) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	_ = mw.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	if caption != "" {
		_ = mw.WriteField("caption", caption)
	}
	fw, err := mw.CreateFormFile("document", filename)
	if err != nil {
		return fmt.Errorf("telegram: sendDocument: form file: %w", err)
	}
	if _, err := fw.Write(content); err != nil {
		return fmt.Errorf("telegram: sendDocument: write: %w", err)
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("telegram: sendDocument: close: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL("sendDocument"), &buf)
	if err != nil {
		return fmt.Errorf("telegram: sendDocument: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return c.doExpectOK(req, "sendDocument")
}

func (c *tgClient) doExpectOK(req *http.Request, method string) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: %s: %w", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram: %s: status %d: %s", method, resp.StatusCode, data)
	}
	return nil
}

// Gateway define a API pública do componente Telegram.
type Gateway interface {
	Start(ctx context.Context) error
	SendApprovalRequest(ctx context.Context, in ApprovalRequest) error
	Notify(ctx context.Context, chatID int64, msg string) error
	NotifyAll(ctx context.Context, msg string) error
	NotifyPR(ctx context.Context, repo string, issueNum int, prURL string) error
	NotifyError(ctx context.Context, repo string, issueNum int, phase string, taskErr error) error
}

type gateway struct {
	cfg         *config.TelegramConfig
	store       *store.Store
	cmds        chan<- domain.Command
	tg          *tgClient
	srv         *http.Server
	secretToken string
}

// New cria e valida o gateway. Retorna erro se qualquer token obrigatório
// estiver ausente na env (tokens nunca são logados).
func New(cfg *config.TelegramConfig, st *store.Store, cmds chan<- domain.Command) (*gateway, error) {
	botToken := os.Getenv(cfg.BotTokenEnv)
	if botToken == "" {
		return nil, fmt.Errorf("telegram: env %q vazia (bot token obrigatorio)", cfg.BotTokenEnv)
	}
	secretToken := os.Getenv(cfg.SecretTokenEnv)
	if secretToken == "" {
		return nil, fmt.Errorf("telegram: env %q vazia (secret token obrigatorio)", cfg.SecretTokenEnv)
	}
	return newWithToken(cfg, st, cmds, botToken, secretToken, "https://api.telegram.org"), nil
}

// newWithToken é o construtor interno — recebe os tokens já resolvidos e a
// base URL do cliente (sobrescrita em testes via httptest.Server).
func newWithToken(cfg *config.TelegramConfig, st *store.Store, cmds chan<- domain.Command, botToken, secretToken, base string) *gateway {
	tg := &tgClient{
		base:     base,
		botToken: botToken,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
	return &gateway{
		cfg:         cfg,
		store:       st,
		cmds:        cmds,
		tg:          tg,
		secretToken: secretToken,
	}
}

// Start registra o webhook no Telegram e inicia o servidor HTTP na porta :8080.
// Bloqueia até ctx ser cancelado; faz Shutdown ao sair.
func (g *gateway) Start(ctx context.Context) error {
	if g.cfg.WebhookURL != "" {
		if err := g.tg.setWebhook(ctx, g.cfg.WebhookURL, g.secretToken); err != nil {
			return fmt.Errorf("telegram: registrar webhook: %w", err)
		}
		slog.Info("telegram: webhook registrado", "url", g.cfg.WebhookURL)
	} else {
		slog.Warn("telegram: webhook_url vazio — setWebhook pulado (modo local)")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /telegram/webhook", g.webhookHandler)

	g.srv = &http.Server{
		Addr:    ":8080",
		Handler: mux,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := g.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		return g.srv.Shutdown(context.Background())
	case err := <-errCh:
		return err
	}
}

// tgUpdate é o mínimo do payload JSON enviado pelo Telegram em cada update.
type tgUpdate struct {
	UpdateID int64      `json:"update_id"`
	Message  *tgMessage `json:"message"`
}

type tgMessage struct {
	Chat tgChat `json:"chat"`
	Text string `json:"text"`
}

type tgChat struct {
	ID int64 `json:"id"`
}

func (g *gateway) webhookHandler(w http.ResponseWriter, r *http.Request) {
	// 1. Validar secret token.
	if r.Header.Get("X-Telegram-Bot-Api-Secret-Token") != g.secretToken {
		slog.Warn("telegram: secret token invalido", "remote_addr", r.RemoteAddr)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// 2. Decodificar update.
	var update tgUpdate
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		slog.Warn("telegram: update invalido", "err", err)
		w.WriteHeader(http.StatusOK)
		return
	}
	if update.Message == nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	chatID := update.Message.Chat.ID
	updateIDStr := strconv.FormatInt(update.UpdateID, 10)

	// 3. Verificar chat autorizado.
	if !g.isAllowed(chatID) {
		slog.Warn("telegram: chat nao autorizado descartado", "chat_id", chatID, "update_id", update.UpdateID)
		w.WriteHeader(http.StatusOK)
		return
	}

	// 4. Idempotência por update_id.
	ctx := r.Context()
	if processed, _ := g.store.WasProcessed(ctx, "tg", updateIDStr); processed {
		slog.Info("telegram: update ja processado", "update_id", update.UpdateID)
		w.WriteHeader(http.StatusOK)
		return
	}

	// 5. Marcar como processado antes de retornar 200.
	if err := g.store.MarkEventProcessed(ctx, "tg", updateIDStr); err != nil {
		slog.Error("telegram: marcar evento processado", "update_id", update.UpdateID, "err", err)
	}

	// 6. Responder 200 imediatamente (o Telegram tem timeout curto).
	w.WriteHeader(http.StatusOK)

	// 7. Processar em goroutine para não bloquear.
	text := update.Message.Text
	go func() {
		bgCtx := context.Background()
		if text == "" {
			return
		}

		slog.Info("telegram: update recebido", "chat_id", chatID, "update_id", update.UpdateID)

		cmd, err := parseCommand(text, chatID)
		if err != nil {
			slog.Warn("telegram: comando invalido", "chat_id", chatID, "text", text, "err", err)
			_ = g.tg.sendMessage(bgCtx, chatID, err.Error(), "HTML")
			return
		}

		slog.Info("telegram: comando parseado", "chat_id", chatID, "update_id", update.UpdateID, "command", string(cmd.Type))

		// /status é tratado diretamente (não precisa do Scheduler).
		if cmd.Type == domain.CmdStatus {
			snap, err := g.store.StatusSnapshot(bgCtx)
			if err != nil {
				slog.Error("telegram: StatusSnapshot", "err", err)
				_ = g.tg.sendMessage(bgCtx, chatID, "Erro ao obter status: "+err.Error(), "HTML")
				return
			}
			_ = g.tg.sendMessage(bgCtx, chatID, formatStatus(snap), "HTML")
			return
		}

		// Demais comandos vão para o Scheduler via canal.
		select {
		case g.cmds <- cmd:
		default:
			slog.Warn("telegram: canal de comandos cheio, descartando", "command", string(cmd.Type), "chat_id", chatID)
		}
	}()
}

func (g *gateway) isAllowed(chatID int64) bool {
	for _, id := range g.cfg.AllowedChatIDs {
		if id == chatID {
			return true
		}
	}
	return false
}

// formatStatus formata um StatusSnapshot como HTML para o /status.
func formatStatus(snap domain.StatusSnapshot) string {
	var sb strings.Builder
	sb.WriteString("<b>Status do Argos</b>\n\n")
	sb.WriteString("<b>Repositorios:</b>\n")
	for _, r := range snap.Repos {
		if r.Paused {
			sb.WriteString(fmt.Sprintf("  %s — pausado (%s)\n", r.Repo, r.PausedReason))
		} else {
			sb.WriteString(fmt.Sprintf("  %s — ativo\n", r.Repo))
		}
	}
	sb.WriteString(fmt.Sprintf("\n<b>Sessoes ativas:</b> %d\n", snap.ActiveSessions))
	if len(snap.PhaseCounts) > 0 {
		sb.WriteString("\n<b>Issues por fase:</b>\n")
		for phase, count := range snap.PhaseCounts {
			sb.WriteString(fmt.Sprintf("  %s: %d\n", phase, count))
		}
	}
	return sb.String()
}

func (g *gateway) SendApprovalRequest(ctx context.Context, in ApprovalRequest) error {
	if err := g.tg.sendMessage(ctx, in.ChatID, FormatDigest(in.Data), "HTML"); err != nil {
		return fmt.Errorf("telegram: SendApprovalRequest digest: %w", err)
	}
	for _, path := range in.SpecFiles {
		content, err := os.ReadFile(path)
		if err != nil {
			slog.Error("telegram: ler arquivo de spec", "path", path, "err", err)
			continue
		}
		filename := filepath.Base(path)
		if err := g.tg.sendDocument(ctx, in.ChatID, filename, content, filename); err != nil {
			slog.Error("telegram: enviar documento", "path", path, "err", err)
		}
	}
	return nil
}

func (g *gateway) Notify(ctx context.Context, chatID int64, msg string) error {
	return g.tg.sendMessage(ctx, chatID, msg, "HTML")
}

func (g *gateway) NotifyAll(ctx context.Context, msg string) error {
	var lastErr error
	for _, chatID := range g.cfg.AllowedChatIDs {
		if err := g.tg.sendMessage(ctx, chatID, msg, "HTML"); err != nil {
			slog.Error("telegram: NotifyAll", "chat_id", chatID, "err", err)
			lastErr = err
		}
	}
	return lastErr
}

func (g *gateway) NotifyPR(ctx context.Context, repo string, issueNum int, prURL string) error {
	msg := fmt.Sprintf("Issue #%d (%s): PR aberto — %s", issueNum, repo, prURL)
	return g.NotifyAll(ctx, msg)
}

func (g *gateway) NotifyError(ctx context.Context, repo string, issueNum int, phase string, taskErr error) error {
	msg := fmt.Sprintf("Issue #%d (%s): falhou na fase %s — %s", issueNum, repo, phase, taskErr.Error())
	return g.NotifyAll(ctx, msg)
}
