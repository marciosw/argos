package runner

import (
	"os"
	"strings"

	"github.com/marciomacedo/argos/internal/telegram"
)

// notInformed é o placeholder quando uma seção do digest não pôde ser extraída.
const notInformed = "(não informado)"

// buildDigest monta o DigestData lendo as seções de design.md (best-effort). Se
// o arquivo não existir ou faltar uma seção, usa placeholders — o humano ainda
// recebe os .md completos como anexos.
func buildDigest(repo string, issue int, title, designPath string) telegram.DigestData {
	d := telegram.DigestData{
		IssueRepo:  repo,
		IssueNum:   issue,
		IssueTitle: firstNonEmpty(title, notInformed),
		Objectives: notInformed,
		Components: notInformed,
		Decisions:  notInformed,
		Risks:      notInformed,
	}

	data, err := os.ReadFile(designPath)
	if err != nil {
		return d
	}
	sections := parseSections(string(data))

	if v := pickSection(sections, "objetiv", "objective", "resumo", "vis"); v != "" {
		d.Objectives = v
	}
	if v := pickSection(sections, "componente", "component", "arquitet"); v != "" {
		d.Components = v
	}
	if v := pickSection(sections, "decis", "decision"); v != "" {
		d.Decisions = v
	}
	if v := pickSection(sections, "risco", "risk", "aten", "pendên", "aberto"); v != "" {
		d.Risks = v
	}
	return d
}

// section preserva o título e o corpo de uma seção markdown na ordem original.
type section struct {
	title string
	body  string
}

// parseSections divide um markdown em seções por heading (linhas iniciadas por
// '#'). O conteúdo antes do primeiro heading é ignorado.
func parseSections(md string) []section {
	var out []section
	var cur *section
	var buf []string

	flush := func() {
		if cur != nil {
			cur.body = strings.TrimSpace(strings.Join(buf, "\n"))
			out = append(out, *cur)
		}
		buf = nil
	}

	for _, line := range strings.Split(md, "\n") {
		if h, ok := headingText(line); ok {
			flush()
			cur = &section{title: h}
			continue
		}
		buf = append(buf, line)
	}
	flush()
	return out
}

// headingText retorna o texto de um heading markdown (sem os '#') e true se a
// linha for um heading.
func headingText(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "#") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimLeft(t, "# ")), true
}

// pickSection devolve o corpo da primeira seção cujo título (lowercase) contém
// qualquer um dos termos. "" se nenhuma casar.
func pickSection(sections []section, terms ...string) string {
	for _, s := range sections {
		lt := strings.ToLower(s.title)
		for _, term := range terms {
			if strings.Contains(lt, term) {
				return s.body
			}
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
