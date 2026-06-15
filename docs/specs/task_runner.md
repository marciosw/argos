# Spec — TaskRunner

> Componente que invoca o **Claude Code CLI** como subprocesso (uma instância por tarefa), parseia o output `stream-json`, monitora o uso da janela de contexto e gerencia a retomada via `progress.md`.

**Status:** rascunho para validação.

> ⚠️ As flags exatas do Claude Code CLI e o schema preciso dos eventos `stream-json` devem ser **confirmados contra a versão instalada** antes da implementação (`claude --help`). Este spec documenta o comportamento esperado e os pontos a verificar.

---

## 1. Responsabilidades

1. Construir e executar o comando `claude` por tarefa, com o modelo resolvido pelo ConfigManager.
2. Injetar o prompt da fase (documentação ou codificação) e os arquivos de contexto.
3. Consumir o `stream-json` em streaming, logando e extraindo `usage` (tokens).
4. Calcular o **% de uso da janela de contexto** antes de avançar de fase / entre blocos de tasks.
5. Encerrar e **retomar** a sessão (nova sessão com `progress.md` + `context.md`) quando uso > 65%.
6. Gerenciar os arquivos de spec por issue e a branch/commits do agente.
7. Reportar status/eventos ao Scheduler e ao TelegramGateway.

## 2. Invocação do Claude Code CLI

Forma esperada (não-interativa, streaming):

```
claude -p "<prompt>" \
  --model <modelo> \
  --output-format stream-json \
  --verbose \
  --dangerously-skip-permissions \
  [--add-dir <repo-path>]
```

Notas (a confirmar com `claude --help`):
- `-p/--print` + `--output-format stream-json` exigem `--verbose` para emitir os eventos completos.
- `--dangerously-skip-permissions`: requisito do projeto (sem prompts de permissão).
- Diretório de trabalho do subprocesso = checkout do repo-alvo (via `cmd.Dir` ou `--add-dir`).
- O prompt pode ser passado por `-p` ou via stdin (preferir stdin para prompts grandes, evitando limite de tamanho de argumento).
- **Retomada**: a estratégia deste projeto é **não** usar `--resume`/`--continue` da mesma sessão (que recarregaria o contexto inteiro). Em vez disso, encerra-se a sessão e abre-se uma **nova** com `progress.md`+`context.md` injetados — reset deliberado de contexto.

### Execução em Go

```go
cmd := exec.CommandContext(ctx, "claude", args...)
cmd.Dir = repoCheckoutPath
stdin → prompt
stdout → parser de stream-json (linha a linha / JSON lines)
stderr → log
```

- `CommandContext` para permitir cancelamento/timeout.
- Ler stdout incrementalmente (`bufio.Scanner` com buffer aumentado, pois linhas JSON podem ser grandes).
- Timeout por fase configurável; ao expirar, encerra o processo (SIGTERM → SIGKILL) e marca erro.

## 3. Parsing do `stream-json`

O `--output-format stream-json` emite **JSON Lines** (um objeto JSON por linha). Esperado (a validar contra a versão):

- Um evento inicial de `system`/`init` (inclui `session_id`, modelo, ferramentas).
- Eventos de `assistant`/`user` (mensagens, com blocos de conteúdo e tool calls).
- Um evento final de `result` com `usage`, `total_cost_usd`, `duration`, etc.

O parser:
- Lê cada linha, faz `json.Unmarshal` num tipo discriminado por `type`.
- Captura `session_id` (para correlação/log; **não** para resume).
- Acumula o **último `usage`** observado (o cliente Claude reporta tokens por mensagem/turno).
- Trata linhas não reconhecidas com tolerância (log em debug, sem quebrar).

```go
type StreamEvent struct {
    Type      string          `json:"type"`        // "system", "assistant", "user", "result", ...
    Subtype   string          `json:"subtype"`
    SessionID string          `json:"session_id"`
    Message   json.RawMessage `json:"message"`     // quando aplicável
    Usage     *Usage          `json:"usage"`       // quando presente
    // ... campos a mapear conforme schema real
}

type Usage struct {
    InputTokens            int `json:"input_tokens"`
    OutputTokens           int `json:"output_tokens"`
    CacheReadInputTokens   int `json:"cache_read_input_tokens"`
    CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}
```

## 4. Cálculo do uso da janela de contexto

O **tamanho do contexto vigente** ≈ soma dos tokens de entrada do último turno:

```
context_tokens = input_tokens + cache_read_input_tokens + cache_creation_input_tokens
uso = context_tokens / janela_do_modelo
```

- `janela_do_modelo`: vem da config do modelo (ex.: 200000 ou 1000000 tokens — depende do modelo resolvido; ver `model_config.md`).
- O `output_tokens` do turno corrente vira input do próximo, então o melhor proxy é o `usage` da **última** mensagem do assistente antes do ponto de decisão.
- **Limiar = 65%**. Configurável (`context_threshold`, default `0.65`).

> Verificação necessária: confirmar que o `stream-json` expõe os campos de `usage` por turno de forma utilizável. Caso a versão não exponha, o fallback é estimar via `count_tokens` (medindo prompt + arquivos de contexto + transcript acumulado) ou via `total_cost_usd`/heurística — **a validar**.

## 5. Pontos de checagem de contexto

A checagem ocorre **antes de avançar entre fases ou blocos de tasks**:
- Transição `documentation → doing`.
- Entre blocos de tasks dentro de `doing` (definição de "bloco" vem do `tasks.md`; sugestão: a cada N tasks concluídas ou ao fim de cada seção do sprint).

Se `uso > limiar`:
1. Instruir/garantir que o `progress.md` esteja atualizado com: o que foi feito, onde parou, próximo passo.
2. Encerrar a sessão atual de forma limpa (finalizar o subprocesso após o turno corrente).
3. Abrir **nova** sessão (novo subprocesso `claude`) cujo prompt inicial injeta `progress.md` + `context.md` (e referência ao `design.md`/`tasks.md`).
4. Registrar no SQLite a contagem de sessões e o motivo do reinício (`resumed_due_to_context`).

## 6. Arquivos de spec por issue

Diretório: `docs/specs/issue-{N}/` (no repo-alvo — a validar):

| Arquivo       | Conteúdo | Quem mantém |
| ------------- | -------- | ----------- |
| `design.md`   | Arquitetura e decisões técnicas da feature. | Claude Code (fase documentation) |
| `tasks.md`    | Tarefas decompostas (sprint). | Claude Code (fase documentation) |
| `progress.md` | Estado atual: feito / onde parou / próximo passo. **Atualizado ao fim de cada sessão** para permitir retomada. | Claude Code (ambas as fases) |
| `context.md`  | Objetivo de negócio, restrições, referências, decisões já tomadas pelo humano (inclui motivos de `/reject`). | Orchestrator + Claude Code |

- O TaskRunner garante a criação do diretório e dos arquivos no início da fase `documentation`.
- `context.md` é **semeado** pelo orquestrador com o corpo da issue e atualizado com o motivo de eventuais rejeições.
- `progress.md` é o artefato-chave da retomada: deve ser conciso e suficiente para reconstruir o estado sem o transcript original.

## 7. Prompts por fase (estrutura)

- **Fase documentation**: instrui o agente a produzir `design.md`, `tasks.md` e a popular `context.md`/`progress.md`, a partir do corpo da issue. Deve terminar com as specs prontas (sem codificar).
- **Fase doing**: instrui o agente a implementar conforme `tasks.md`/`design.md`, atualizar `progress.md` ao fim de cada bloco, **criar branch `agent/issue-{N}` e commitar** (nunca push na `main`), e parar quando o sprint estiver concluído.
- **Retomada**: prompt que injeta `progress.md` + `context.md` + referência aos demais arquivos e instrui a continuar do "próximo passo".

Os templates de prompt ficam versionados (ex.: `internal/runner/prompts/`).

## 8. API interna do componente

```go
type Runner interface {
    // Executa a fase de documentação; retorna quando as specs estão prontas.
    RunDocumentation(ctx, task Task) (DocResult, error)

    // Executa a fase de codificação; pode reiniciar sessões internamente por contexto.
    RunCoding(ctx, task Task) (CodeResult, error)
}

type Task struct {
    Repo        string
    Issue       int
    RepoPath    string   // checkout local do repo-alvo
    Model       string   // resolvido pelo ConfigManager
    ContextWindow int    // janela do modelo (tokens)
}

type DocResult struct {
    SpecDir     string
    Digest      Digest   // objetivos, componentes, decisões, atenção
    Files       []string // caminhos dos .md gerados
    Sessions    int
}

type CodeResult struct {
    Branch   string
    Commits  int
    Sessions int
    Done     bool
}
```

## 9. Concorrência e isolamento

- **Uma instância por tarefa**: cada `Run*` cria seu próprio subprocesso, seu próprio checkout/working dir e seus próprios arquivos.
- O Scheduler controla o nº de tarefas simultâneas (semáforo).
- Cada subprocesso tem `context.Context` próprio para cancelamento (ex.: `/pause` ou shutdown).

## 10. Tratamento de erros

- Exit code ≠ 0 do `claude`: capturar stderr, marcar `agent:error`, notificar Telegram, preservar `progress.md` para retomada manual.
- Timeout de fase: encerrar processo, gravar estado, notificar.
- `stream-json` malformado: tolerância no parser; se o stream encerrar sem evento `result`, tratar como falha.
- Falha de git (branch/commit): reportar; não tentar push forçado.

## 11. Pontos abertos para validação

1. **Schema real do `stream-json`** e disponibilidade dos campos de `usage` por turno (verificar `claude --help` e um run de teste).
2. Como passar o prompt: `-p` vs. stdin (sugestão: stdin).
3. Definição de "bloco de tasks" para a checagem intermediária de contexto.
4. Local dos checkouts dos repos-alvo (worktrees? clones dedicados por tarefa?).
5. Limiar de contexto configurável — confirmar 65% como default.
6. Estratégia se o `usage` não vier no stream (fallback de estimativa).
