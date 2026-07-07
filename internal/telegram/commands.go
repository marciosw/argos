package telegram

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/marciomacedo/argos/internal/domain"
)

// parseCommand parseia text como um comando do Telegram e retorna o
// domain.Command correspondente. Função pura — sem efeitos colaterais.
// chatID é sempre preenchido no Command retornado.
func parseCommand(text string, chatID int64) (domain.Command, error) {
	text = strings.TrimSpace(text)
	if text == "" || text[0] != '/' {
		return domain.Command{}, fmt.Errorf("mensagem nao e um comando — use /approve, /reject, /pause, /resume, /status ou /run")
	}

	parts := strings.Fields(text)
	if len(parts) == 0 {
		return domain.Command{}, fmt.Errorf("comando vazio")
	}

	// Strip do sufixo @botname: /cmd@bot arg → /cmd arg
	rawCmd := parts[0]
	if idx := strings.IndexByte(rawCmd, '@'); idx >= 0 {
		rawCmd = rawCmd[:idx]
	}
	cmd := strings.ToLower(rawCmd)
	args := parts[1:]

	base := domain.Command{ChatID: chatID}

	switch cmd {
	case "/approve":
		n, err := parseIssueNum(args)
		if err != nil {
			return domain.Command{}, fmt.Errorf("/approve: %w\nUso: /approve #N", err)
		}
		base.Type = domain.CmdApprove
		base.Issue = n

	case "/reject":
		if len(args) < 2 {
			return domain.Command{}, fmt.Errorf("/reject: motivo e obrigatorio\nUso: /reject #N motivo")
		}
		n, err := parseIssueNum(args[:1])
		if err != nil {
			return domain.Command{}, fmt.Errorf("/reject: %w\nUso: /reject #N motivo", err)
		}
		base.Type = domain.CmdReject
		base.Issue = n
		base.Reason = strings.Join(args[1:], " ")

	case "/pause":
		repo, err := parseRepo(args)
		if err != nil {
			return domain.Command{}, fmt.Errorf("/pause: %w\nUso: /pause web|mobile|hybrid", err)
		}
		base.Type = domain.CmdPause
		base.Repo = repo

	case "/resume":
		repo, err := parseRepo(args)
		if err != nil {
			return domain.Command{}, fmt.Errorf("/resume: %w\nUso: /resume web|mobile|hybrid", err)
		}
		base.Type = domain.CmdResume
		base.Repo = repo

	case "/status":
		base.Type = domain.CmdStatus

	case "/run":
		n, err := parseIssueNum(args)
		if err != nil {
			return domain.Command{}, fmt.Errorf("/run: %w\nUso: /run #N", err)
		}
		base.Type = domain.CmdRun
		base.Issue = n

	default:
		return domain.Command{}, fmt.Errorf(
			"comando desconhecido %q\nComandos disponiveis: /approve, /reject, /pause, /resume, /status, /run, /preview",
			cmd,
		)
	}

	return base, nil
}

// parseIssueNum extrai o número de issue de args[0], aceitando "#N" ou "N".
func parseIssueNum(args []string) (int, error) {
	if len(args) == 0 {
		return 0, fmt.Errorf("numero da issue e obrigatorio (ex: #42 ou 42)")
	}
	s := strings.TrimPrefix(args[0], "#")
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("numero de issue invalido: %q (ex: #42 ou 42)", args[0])
	}
	return n, nil
}

// ============================ /preview ==============================

// PreviewAction é a sub-ação do comando /preview.
type PreviewAction string

const (
	PreviewStart  PreviewAction = "start"
	PreviewStop   PreviewAction = "stop"
	PreviewStatus PreviewAction = "status"
)

// PreviewCommand é um comando /preview já parseado.
type PreviewCommand struct {
	Action  PreviewAction
	IssueID int // para start/stop; 0 para status
	ChatID  int64
}

// parsePreviewCommand tenta parsear text como /preview [...].
//   - Retorna (zero, false, nil) se o texto não começa com /preview.
//   - Retorna (zero, true, err) se começa com /preview mas tem erro de sintaxe.
//   - Retorna (cmd, true, nil) em caso de sucesso.
func parsePreviewCommand(text string, chatID int64) (PreviewCommand, bool, error) {
	text = strings.TrimSpace(text)
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return PreviewCommand{}, false, nil
	}

	rawCmd := parts[0]
	if idx := strings.IndexByte(rawCmd, '@'); idx >= 0 {
		rawCmd = rawCmd[:idx]
	}
	if strings.ToLower(rawCmd) != "/preview" {
		return PreviewCommand{}, false, nil
	}

	base := PreviewCommand{ChatID: chatID}
	args := parts[1:]

	if len(args) == 0 {
		return PreviewCommand{}, true, fmt.Errorf(
			"/preview: acao obrigatoria\nUso: /preview #N | /preview stop #N | /preview status")
	}

	switch strings.ToLower(args[0]) {
	case "status":
		base.Action = PreviewStatus
		return base, true, nil

	case "stop":
		if len(args) < 2 {
			return PreviewCommand{}, true, fmt.Errorf(
				"/preview stop: numero de issue obrigatorio (ex: /preview stop #42)")
		}
		n, err := parsePreviewIssueNum(args[1])
		if err != nil {
			return PreviewCommand{}, true, fmt.Errorf("/preview stop: %w", err)
		}
		base.Action = PreviewStop
		base.IssueID = n
		return base, true, nil

	default:
		// Interpreta como /preview #N (start) — # obrigatório.
		n, err := parsePreviewIssueNum(args[0])
		if err != nil {
			return PreviewCommand{}, true, fmt.Errorf("/preview: %w", err)
		}
		base.Action = PreviewStart
		base.IssueID = n
		return base, true, nil
	}
}

// parsePreviewIssueNum extrai o número de issue de s, exigindo o prefixo '#'.
func parsePreviewIssueNum(s string) (int, error) {
	if !strings.HasPrefix(s, "#") {
		return 0, fmt.Errorf("numero de issue deve ter prefixo '#' (ex: #42), recebido %q", s)
	}
	n, err := strconv.Atoi(strings.TrimPrefix(s, "#"))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("numero de issue invalido: %q (ex: #42)", s)
	}
	return n, nil
}

// parseRepo valida que args[0] é um repositório reconhecido.
func parseRepo(args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("repositorio e obrigatorio (redeagenda)")
	}
	repo := strings.ToLower(args[0])
	if !domain.IsValidRepo(repo) {
		return "", fmt.Errorf("repositorio invalido %q — use: redeagenda", args[0])
	}
	return repo, nil
}
