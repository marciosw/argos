package runner

import (
	"encoding/json"
	"log/slog"
)

// StreamEvent é um evento do output `--output-format stream-json` (JSON Lines:
// um objeto JSON por linha). Os campos refletem o schema REAL confirmado contra
// o CLI instalado (claude 2.1.x):
//
//   - {"type":"system","subtype":"init","session_id":...,"model":...,"tools":[...]}
//   - {"type":"rate_limit_event",...}                       (tolerado/ignorado)
//   - {"type":"assistant","message":{...,"usage":{...}},...} usage por turno
//   - {"type":"result","subtype":"success","is_error":false,"usage":{...},...}
//
// Divergência importante vs. a spec §3: o `usage` por turno vem ANINHADO em
// `message.usage` nos eventos assistant; no evento `result` ele aparece também
// no nível superior. Capturamos ambos.
type StreamEvent struct {
	Type      string         `json:"type"`
	Subtype   string         `json:"subtype"`
	SessionID string         `json:"session_id"`
	IsError   bool           `json:"is_error"` // presente no evento result
	Message   *streamMessage `json:"message"`  // assistant/user
	Usage     *Usage         `json:"usage"`    // nível superior (result)
}

type streamMessage struct {
	Usage *Usage `json:"usage"`
}

// Usage são os contadores de tokens de um turno (campos confirmados no schema
// real; nomes idênticos à spec §3).
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// streamParser consome o stream-json linha a linha, de forma tolerante a
// linhas não reconhecidas (não quebra). Acumula o ÚLTIMO usage observado e
// captura o session_id (só para correlação/log; NÃO para --resume).
type streamParser struct {
	lastUsage Usage
	sawUsage  bool
	sessionID string
	sawResult bool
	resultErr bool
}

// feed processa uma linha de stdout. Linhas malformadas são logadas em debug e
// ignoradas (tolerância — spec §3 e §10).
func (p *streamParser) feed(line string) {
	if line == "" {
		return
	}
	var ev StreamEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		slog.Debug("runner: linha stream-json não reconhecida", "err", err)
		return
	}
	if ev.SessionID != "" {
		p.sessionID = ev.SessionID
	}
	// usage por turno: assistant traz em message.usage; result no topo.
	if ev.Message != nil && ev.Message.Usage != nil {
		p.lastUsage = *ev.Message.Usage
		p.sawUsage = true
	} else if ev.Usage != nil {
		p.lastUsage = *ev.Usage
		p.sawUsage = true
	}
	if ev.Type == "result" {
		p.sawResult = true
		p.resultErr = ev.IsError
	}
}

// contextUsage calcula a fração da janela de contexto ocupada pelo último turno
// (spec §4): (input + cache_read + cache_creation) / janela. O output do turno
// corrente vira input do próximo, então o melhor proxy é o usage da última
// mensagem. Retorna 0 se a janela for inválida.
func contextUsage(u Usage, window int) float64 {
	if window <= 0 {
		return 0
	}
	ctxTokens := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
	return float64(ctxTokens) / float64(window)
}
