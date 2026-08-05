package runner

import (
	"strings"
	"text/template"
)

// promptData alimenta os templates de prompt por fase.
type promptData struct {
	Repo       string
	Issue      int
	SpecDir    string // ex.: docs/specs/issue-42 (relativo ao repo-alvo)
	Branch     string // ex.: agent/issue-42 (fase doing)
	BaseBranch string // ex.: main
}

// Templates de prompt por fase, versionados no pacote (spec §7). Mantidos
// simples e em pt-BR, alinhados ao CLAUDE.md do projeto.

var docTemplate = template.Must(template.New("doc").Parse(strings.TrimSpace(`
Você é um agente de documentação trabalhando na issue #{{.Issue}} do repositório "{{.Repo}}".

Objetivo: produzir as specs da feature em {{.SpecDir}}/ a partir do corpo da issue
e de {{.SpecDir}}/context.md (objetivo de negócio, restrições, decisões do humano).

Entregue, em {{.SpecDir}}/:
- design.md  — arquitetura e decisões técnicas (com seções: Objetivos, Componentes,
  Decisões técnicas, Riscos / pontos de atenção).
- tasks.md   — tarefas decompostas (sprint).
- progress.md — estado atual: feito / onde parou / próximo passo.
- context.md — complemente o que já existir, sem apagar decisões do humano.

NÃO escreva código de produção nesta fase. Ao concluir as specs, pare.
Mantenha o progress.md sempre atualizado o suficiente para retomar sem este histórico.
`)))

var codingTemplate = template.Must(template.New("coding").Parse(strings.TrimSpace(`
Você é um agente de codificação trabalhando na issue #{{.Issue}} do repositório "{{.Repo}}".

Implemente conforme {{.SpecDir}}/tasks.md e {{.SpecDir}}/design.md.

Regras:
- Trabalhe na branch "{{.Branch}}" (já criada). NUNCA faça push direto em "{{.BaseBranch}}".
- Faça commits coesos conforme avança.
- Atualize {{.SpecDir}}/progress.md ao fim de cada bloco de tarefas.
- Pare quando o sprint de tasks.md estiver concluído.
`)))

var resumeTemplate = template.Must(template.New("resume").Parse(strings.TrimSpace(`
Você está RETOMANDO o trabalho na issue #{{.Issue}} do repositório "{{.Repo}}".
Esta é uma sessão nova, com contexto reiniciado de forma deliberada.

Leia, em {{.SpecDir}}/:
- progress.md — onde o trabalho parou e qual o próximo passo (ponto de partida).
- context.md  — objetivo de negócio, restrições e decisões do humano.
- design.md / tasks.md — referência da arquitetura e do sprint.

Continue exatamente do "próximo passo" indicado em progress.md.
{{if .Branch}}Trabalhe na branch "{{.Branch}}"; NUNCA faça push em "{{.BaseBranch}}".
Faça commits conforme avança e atualize progress.md ao fim de cada bloco.{{else}}Atualize progress.md ao fim do trabalho. NÃO escreva código de produção nesta fase de documentação.{{end}}
`)))

func renderPrompt(t *template.Template, d promptData) string {
	var sb strings.Builder
	// Os templates são estáticos e válidos; erro de execução é irrelevante aqui.
	_ = t.Execute(&sb, d)
	return sb.String()
}
