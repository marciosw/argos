// Package telegram implementa o gateway de comando e validação via Telegram
// Bot API (modo webhook): recebe comandos, envia digests e arquivos, e gerencia
// aprovações pendentes.
package telegram

import "fmt"

// DigestData contém os campos estruturados do digest de uma spec.
type DigestData struct {
	IssueRepo  string
	IssueNum   int
	IssueTitle string
	Objectives string // extraído de docs/specs/issue-{N}/design.md
	Components string
	Decisions  string
	Risks      string
}

// ApprovalRequest agrupa tudo que o gateway precisa para enviar uma
// solicitação de aprovação de spec ao humano.
type ApprovalRequest struct {
	ChatID    int64
	Data      DigestData
	SpecFiles []string // caminhos absolutos dos .md a enviar como documentos
}

// FormatDigest formata d como HTML (parse_mode=HTML) com os campos de
// DigestData estruturados em seções, terminando com a instrução de aprovação.
func FormatDigest(d DigestData) string {
	return fmt.Sprintf(
		"<b>Spec pronta — Issue #%d (%s)</b>\n"+
			"<b>Titulo:</b> %s\n\n"+
			"<b>Objetivos</b>\n%s\n\n"+
			"<b>Componentes</b>\n%s\n\n"+
			"<b>Decisoes tecnicas</b>\n%s\n\n"+
			"<b>Riscos / pontos de atencao</b>\n%s\n\n"+
			"Responda /approve #%d ou /reject #%d motivo",
		d.IssueNum, d.IssueRepo,
		d.IssueTitle,
		d.Objectives,
		d.Components,
		d.Decisions,
		d.Risks,
		d.IssueNum, d.IssueNum,
	)
}
