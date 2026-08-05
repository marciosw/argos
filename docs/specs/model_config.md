# Spec — ConfigManager (configuração de modelo)

> Resolução do modelo usado pelo Claude Code CLI, configurável por variável de ambiente e/ou arquivo, com override por repositório e por issue.

**Status:** rascunho para validação.

---

## 1. Responsabilidades

1. Resolver, para cada tarefa, o **modelo** e parâmetros associados (janela de contexto, limiar).
2. Suportar configuração em camadas com precedência clara: **issue > repo > env > arquivo > default**.
3. Validar valores e fornecer a janela de contexto correta ao TaskRunner (para o cálculo de uso).

## 2. Modelo padrão

- **Default do projeto:** `claude-opus-4-5` (perfil **opusplan** — Opus para planejamento/raciocínio).
- ⚠️ **Nota de validação:** `claude-opus-4-5` está ativo, porém o Opus mais capaz atual é `claude-opus-4-8`. Recomenda-se considerar `claude-opus-4-8` como default. Mantido `claude-opus-4-5` neste spec conforme pedido — **confirmar na validação**.
- "opusplan" é um alias do Claude Code (Opus no planejamento, modelo mais leve na execução). A config deve aceitar tanto **aliases do Claude Code** (`opus`, `sonnet`, `opusplan`, ...) quanto **model IDs explícitos** (`claude-opus-4-5`, `claude-opus-4-8`, `claude-sonnet-4-6`, ...). O ConfigManager **não** valida o ID contra um catálogo fechado (para não quebrar quando novos modelos surgirem); apenas repassa ao `--model` do `claude` e usa a janela de contexto declarada.

## 3. Precedência (resolução em camadas)

Do mais específico ao mais genérico (o primeiro que definir vence):

```
1. Override por ISSUE      (config dinâmica, ex.: gravada via comando/arquivo por issue)
2. Override por REPO       (bloco do repo no arquivo de config)
3. Variável de AMBIENTE    (ORCHESTRATOR_MODEL)
4. Arquivo de config       (campo model.default)
5. DEFAULT embutido        (claude-opus-4-5)
```

## 4. Fontes de configuração

### 4.1 Variáveis de ambiente

| Env var                          | Efeito |
| -------------------------------- | ------ |
| `ORCHESTRATOR_MODEL`             | Sobrescreve o modelo default global. |
| `ORCHESTRATOR_MODEL_WEB`         | Override do modelo para o repo `web`. |
| `ORCHESTRATOR_MODEL_MOBILE`      | Override para `mobile`. |
| `ORCHESTRATOR_MODEL_HYBRID`      | Override para `hybrid`. |
| `ORCHESTRATOR_CONTEXT_THRESHOLD` | Limiar de uso de contexto (ex.: `0.65`). |

(Env vars de override por repo são um atalho; o canônico é o arquivo.)

### 4.2 Arquivo de configuração (YAML — proposta)

```yaml
model:
  default: "claude-opus-4-5"        # opusplan
  context_window: 200000            # janela do modelo default (tokens)
  context_threshold: 0.65           # limiar de retomada

  # janelas por modelo (para o cálculo de uso); chave = id/alias
  windows:
    claude-opus-4-5: 200000
    claude-opus-4-8: 1000000
    claude-sonnet-4-6: 1000000
    opus: 200000
    opusplan: 200000

  # overrides por repositório
  repos:
    web:    { model: "claude-opus-4-5" }
    mobile: { model: "claude-opus-4-5" }
    hybrid: { model: "claude-opus-4-5" }

  # overrides por issue (opcional; pode também ser setado em runtime)
  issues:
    # "web#42": { model: "claude-opus-4-8" }
```

> As janelas de contexto por modelo são declaradas na config (não hardcoded), para permitir ajuste sem recompilar e para acompanhar modelos novos. Valores acima são exemplos — **confirmar os valores reais na validação** (ver nota em §7).

### 4.3 Override por issue em runtime

- Mecanismo a definir (a validar). Opções:
  - Campo `issues."<repo>#<n>"` no arquivo de config (recarregável).
  - Coluna opcional `model` na tabela `issues` do SQLite, setável por um comando futuro do Telegram.
- Sugestão para v1: suportar override por issue **via arquivo de config**; deixar comando do Telegram para depois.

## 5. API interna do componente

```go
type ModelConfig struct {
    Model            string  // id ou alias repassado ao --model
    ContextWindow    int     // tokens (para cálculo de uso)
    ContextThreshold float64 // 0..1
}

type ConfigManager interface {
    // Resolve o modelo efetivo para um repo+issue, aplicando a precedência.
    Resolve(repo string, issue int) ModelConfig

    // Recarrega o arquivo de config (hot reload opcional).
    Reload() error
}
```

- `Resolve` nunca falha: se nada for definido, retorna o default embutido.
- Se a janela do modelo não estiver em `windows`, usar `model.context_window` (ou um fallback conservador) e logar aviso.

## 6. Integração com o TaskRunner

- O Scheduler chama `Resolve(repo, issue)` ao criar a `Task`.
- O `ModelConfig.Model` vira o argumento `--model` do `claude`.
- `ContextWindow` + `ContextThreshold` alimentam o cálculo de uso (ver `task_runner.md` §4–5).

## 7. Pontos abertos para validação

1. **Default**: manter `claude-opus-4-5` (opusplan) ou atualizar para `claude-opus-4-8`?
2. **Aliases vs. IDs**: confirmar que o Claude Code instalado aceita `opusplan`/`opus` como `--model` e como esses interagem com a janela de contexto para o cálculo de uso.
3. **Janelas de contexto reais** por modelo (os valores no exemplo precisam ser confirmados contra a documentação do modelo em uso).
4. **Override por issue**: via arquivo (v1) e/ou via comando do Telegram (futuro)?
5. **Hot reload** do arquivo de config: necessário na v1?
6. Formato do arquivo: YAML vs. TOML (sugestão: YAML, alinhado aos demais exemplos).
