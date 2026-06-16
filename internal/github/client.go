// Package github contém o cliente REST mínimo para a GitHub API v3, o Poller
// de issues/labels e a abertura de Pull Requests.
// Token Bearer nunca é logado; lido do ambiente pelo chamador.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

const (
	hdrAccept     = "application/vnd.github+json"
	hdrAPIVersion = "2022-11-28"
	reqTimeout    = 30 * time.Second
	maxRetries    = 4
)

// APIError é retornado quando a API responde com status >= 400 e sem retry.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("github: HTTP %d: %s", e.StatusCode, e.Message)
}

// RateLimit contém os dados de rate limit dos headers de resposta.
type RateLimit struct {
	Remaining int
	Reset     time.Time
	Limit     int
}

// Client é o cliente HTTP mínimo para a GitHub REST API v3.
// O Bearer token nunca é logado.
type Client struct {
	base  string
	token string
	http  *http.Client
	// backoff calcula a espera antes da tentativa n (0-indexed).
	// Sobrescrito em testes para eliminar sleeps reais.
	backoff func(n int) time.Duration
}

// NewClient cria um Client com base URL e Bearer token.
func NewClient(base, token string) *Client {
	return &Client{
		base:    base,
		token:   token,
		http:    &http.Client{Timeout: reqTimeout},
		backoff: defaultBackoff,
	}
}

// WithInstantBackoff substitui o backoff por espera zero (para testes).
func (c *Client) WithInstantBackoff() *Client {
	c.backoff = func(int) time.Duration { return 0 }
	return c
}

func defaultBackoff(n int) time.Duration {
	return time.Duration(1<<uint(n)) * time.Second
}

// do executa method+path com body JSON opcional, decodificando em out.
// Faz até maxRetries tentativas com backoff exponencial em 5xx/429.
func (c *Client) do(ctx context.Context, method, path string, body interface{}, out interface{}) (*RateLimit, error) {
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("github: marshal body: %w", err)
		}
	}

	var lastRL *RateLimit
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		rl, retry, retryAfter, err := c.once(ctx, method, path, bodyBytes, out)
		if rl != nil {
			lastRL = rl
		}
		if err == nil {
			return lastRL, nil
		}
		lastErr = err
		if !retry || attempt+1 >= maxRetries {
			break
		}
		wait := retryAfter
		if wait < 0 {
			wait = c.backoff(attempt)
		}
		slog.Warn("github: retry", "attempt", attempt+1, "wait", wait, "path", path, "error", err)
		select {
		case <-ctx.Done():
			return lastRL, ctx.Err()
		case <-time.After(wait):
		}
	}
	return lastRL, lastErr
}

// once executa uma única tentativa. Retorna (rateLimit, shouldRetry, retryAfter, error).
// retryAfter == -1 significa "usar backoff padrão".
func (c *Client) once(ctx context.Context, method, path string, bodyBytes []byte, out interface{}) (*RateLimit, bool, time.Duration, error) {
	var reqBody io.Reader
	if len(bodyBytes) > 0 {
		reqBody = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reqBody)
	if err != nil {
		return nil, false, -1, fmt.Errorf("github: new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", hdrAccept)
	req.Header.Set("X-GitHub-Api-Version", hdrAPIVersion)
	if len(bodyBytes) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, true, -1, fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	rl := parseRateLimit(resp)

	switch {
	case resp.StatusCode == 429:
		return rl, true, parseRetryAfter(resp), &APIError{StatusCode: 429, Message: "rate limited"}
	case resp.StatusCode >= 500:
		return rl, true, -1, &APIError{StatusCode: resp.StatusCode, Message: "server error"}
	case resp.StatusCode >= 400:
		return rl, false, -1, &APIError{StatusCode: resp.StatusCode, Message: readMsg(resp.Body)}
	}

	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return rl, false, -1, fmt.Errorf("github: decode %s %s: %w", method, path, err)
		}
	}
	return rl, false, -1, nil
}

func parseRateLimit(resp *http.Response) *RateLimit {
	rl := &RateLimit{}
	if v := resp.Header.Get("X-RateLimit-Remaining"); v != "" {
		rl.Remaining, _ = strconv.Atoi(v)
	}
	if v := resp.Header.Get("X-RateLimit-Limit"); v != "" {
		rl.Limit, _ = strconv.Atoi(v)
	}
	if v := resp.Header.Get("X-RateLimit-Reset"); v != "" {
		if ts, _ := strconv.ParseInt(v, 10, 64); ts > 0 {
			rl.Reset = time.Unix(ts, 0)
		}
	}
	return rl
}

// parseRetryAfter retorna a duração indicada pelo header Retry-After,
// ou -1 se o header estiver ausente/inválido.
func parseRetryAfter(resp *http.Response) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return -1
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	return -1
}

func readMsg(r io.Reader) string {
	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r).Decode(&body); err == nil && body.Message != "" {
		return body.Message
	}
	return "unknown error"
}
