package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/marciomacedo/argos/internal/config"
)

func newTestPoller(t *testing.T, handler http.Handler) Poller {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	client := NewClient(ts.URL, "test-token").WithInstantBackoff()
	repos := map[string]config.RepoTarget{
		"web": {Owner: "org", Name: "repo-web"},
	}
	return NewPoller(client, repos)
}

// writeJSON encodes v as JSON into w (helper for test handlers).
func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

// --- ListByLabels ------------------------------------------------------------

func TestListByLabels(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/org/repo-web/issues" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		// Verify mandatory headers.
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Accept"); got != hdrAccept {
			t.Errorf("Accept = %q", got)
		}
		if got := r.Header.Get("X-GitHub-Api-Version"); got != hdrAPIVersion {
			t.Errorf("X-GitHub-Api-Version = %q", got)
		}
		// Verify labels query param contains both labels.
		labels := r.URL.Query().Get("labels")
		if labels != "agent:ready,todo" && labels != "todo,agent:ready" {
			t.Errorf("labels query = %q", labels)
		}
		writeJSON(w, []ghIssue{
			{Number: 1, Title: "Fix bug", Body: "body1", Labels: []ghLabel{{Name: "agent:ready"}}, HTMLURL: "https://github.com/org/repo-web/issues/1"},
			{Number: 2, Title: "Feature", Body: "body2", Labels: []ghLabel{{Name: "todo"}, {Name: "bug"}}, HTMLURL: "https://github.com/org/repo-web/issues/2"},
		})
	})

	p := newTestPoller(t, handler)
	issues, _, err := p.ListByLabels(context.Background(), "web", []string{"agent:ready", "todo"}, "")
	if err != nil {
		t.Fatalf("ListByLabels: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("got %d issues, want 2", len(issues))
	}

	i0 := issues[0]
	if i0.Number != 1 || i0.Repo != "web" || i0.Title != "Fix bug" {
		t.Errorf("issues[0] = %+v", i0)
	}
	if len(i0.Labels) != 1 || i0.Labels[0] != "agent:ready" {
		t.Errorf("issues[0].Labels = %v", i0.Labels)
	}
	if i0.URL != "https://github.com/org/repo-web/issues/1" {
		t.Errorf("issues[0].URL = %q", i0.URL)
	}

	i1 := issues[1]
	if i1.Number != 2 || len(i1.Labels) != 2 {
		t.Errorf("issues[1] = %+v", i1)
	}
}

func TestListByLabelsEmpty(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []ghIssue{})
	})
	p := newTestPoller(t, handler)
	issues, _, err := p.ListByLabels(context.Background(), "web", []string{"agent:ready"}, "")
	if err != nil {
		t.Fatalf("ListByLabels: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("expected 0 issues, got %d", len(issues))
	}
}

func TestListByLabelsUnknownRepo(t *testing.T) {
	p := newTestPoller(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	_, _, err := p.ListByLabels(context.Background(), "nonexistent", []string{"todo"}, "")
	if err == nil {
		t.Fatal("expected error for unknown repo")
	}
}

// --- TransitionLabel ---------------------------------------------------------

func TestTransitionLabel(t *testing.T) {
	var putBody []byte
	var putCalled int

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/org/repo-web/issues/42/labels":
			writeJSON(w, []ghLabel{{Name: "agent:ready"}, {Name: "bug"}})
		case r.Method == http.MethodPut && r.URL.Path == "/repos/org/repo-web/issues/42/labels":
			putCalled++
			putBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	p := newTestPoller(t, handler)
	if err := p.TransitionLabel(context.Background(), "web", 42, "agent:ready", "documentation"); err != nil {
		t.Fatalf("TransitionLabel: %v", err)
	}

	if putCalled != 1 {
		t.Errorf("PUT called %d times, want 1", putCalled)
	}

	var body map[string][]string
	if err := json.Unmarshal(putBody, &body); err != nil {
		t.Fatalf("unmarshal PUT body: %v", err)
	}
	lbls := body["labels"]
	if !contains(lbls, "documentation") {
		t.Errorf("PUT labels missing 'documentation': %v", lbls)
	}
	if contains(lbls, "agent:ready") {
		t.Errorf("PUT labels still contains 'agent:ready': %v", lbls)
	}
	if !contains(lbls, "bug") {
		t.Errorf("PUT labels missing 'bug': %v", lbls)
	}
}

func TestTransitionLabelNoop(t *testing.T) {
	// fromLabel ausente + toLabel já presente → nenhum PUT.
	var putCalled int
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, []ghLabel{{Name: "documentation"}})
		} else if r.Method == http.MethodPut {
			putCalled++
			w.WriteHeader(http.StatusOK)
		}
	})

	p := newTestPoller(t, handler)
	if err := p.TransitionLabel(context.Background(), "web", 1, "agent:ready", "documentation"); err != nil {
		t.Fatalf("TransitionLabel: %v", err)
	}
	if putCalled != 0 {
		t.Errorf("unexpected PUT call (expected noop)")
	}
}

func TestComputeTransition(t *testing.T) {
	cases := []struct {
		current []string
		from    string
		to      string
		want    []string
	}{
		{[]string{"agent:ready", "bug"}, "agent:ready", "documentation", []string{"bug", "documentation"}},
		{[]string{"documentation"}, "agent:ready", "documentation", []string{"documentation"}}, // noop set
		{[]string{"bug"}, "agent:ready", "documentation", []string{"bug", "documentation"}},    // from absent, to added
		{[]string{"agent:ready"}, "agent:ready", "todo", []string{"todo"}},                     // only from present
		{[]string{"doing", "agent:ready"}, "agent:ready", "documentation", []string{"doing", "documentation"}},
	}
	for _, tc := range cases {
		got := computeTransition(tc.current, tc.from, tc.to)
		if !setEq(got, tc.want) {
			t.Errorf("computeTransition(%v, %q, %q) = %v, want %v", tc.current, tc.from, tc.to, got, tc.want)
		}
	}
}

// --- Retry -------------------------------------------------------------------

func TestRetry429(t *testing.T) {
	var count int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&count, 1)
		if n == 1 {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeJSON(w, []ghIssue{})
	})

	p := newTestPoller(t, handler)
	issues, _, err := p.ListByLabels(context.Background(), "web", []string{"todo"}, "")
	if err != nil {
		t.Fatalf("ListByLabels after retry: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("expected 0 issues, got %d", len(issues))
	}
	if n := atomic.LoadInt32(&count); n < 2 {
		t.Errorf("expected ≥2 requests, got %d", n)
	}
}

func TestRetry429WithRetryAfter(t *testing.T) {
	var count int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&count, 1)
		if n <= 2 {
			w.Header().Set("Retry-After", "0") // wait 0s → immediate in tests
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeJSON(w, []ghIssue{})
	})

	p := newTestPoller(t, handler)
	_, _, err := p.ListByLabels(context.Background(), "web", []string{"todo"}, "")
	if err != nil {
		t.Fatalf("ListByLabels: %v", err)
	}
	if n := atomic.LoadInt32(&count); n != 3 {
		t.Errorf("expected 3 requests, got %d", n)
	}
}

func TestRetry5xx(t *testing.T) {
	var count int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&count, 1)
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		writeJSON(w, []ghIssue{})
	})

	p := newTestPoller(t, handler)
	_, _, err := p.ListByLabels(context.Background(), "web", []string{"todo"}, "")
	if err != nil {
		t.Fatalf("ListByLabels after 5xx retry: %v", err)
	}
	if n := atomic.LoadInt32(&count); n != 3 {
		t.Errorf("expected 3 requests (2 fail + 1 ok), got %d", n)
	}
}

func TestRetryExhausted(t *testing.T) {
	var count int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&count, 1)
		w.WriteHeader(http.StatusInternalServerError)
	})

	p := newTestPoller(t, handler)
	_, _, err := p.ListByLabels(context.Background(), "web", []string{"todo"}, "")
	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}
	if n := atomic.LoadInt32(&count); n != maxRetries {
		t.Errorf("expected %d requests, got %d", maxRetries, n)
	}
}

func TestNo4xxRetry(t *testing.T) {
	var count int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&count, 1)
		w.WriteHeader(http.StatusUnprocessableEntity)
		writeJSON(w, map[string]string{"message": "Validation Failed"})
	})

	p := newTestPoller(t, handler)
	_, _, err := p.ListByLabels(context.Background(), "web", []string{"todo"}, "")
	if err == nil {
		t.Fatal("expected error on 422")
	}
	if n := atomic.LoadInt32(&count); n != 1 {
		t.Errorf("4xx must not retry: expected 1 request, got %d", n)
	}
}

func TestRateLimitHeaders(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "42")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Reset", "1700000000")
		writeJSON(w, []ghIssue{})
	})

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	client := NewClient(ts.URL, "tok")
	repos := map[string]config.RepoTarget{"web": {Owner: "o", Name: "r"}}
	p := NewPoller(client, repos).(*poller)

	var raw []ghIssue
	rl, err := p.client.do(context.Background(), "GET", "/repos/o/r/issues?state=open&labels=todo&per_page=100", nil, &raw)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if rl.Remaining != 42 {
		t.Errorf("Remaining = %d, want 42", rl.Remaining)
	}
	if rl.Limit != 5000 {
		t.Errorf("Limit = %d, want 5000", rl.Limit)
	}
	if rl.Reset.IsZero() {
		t.Error("Reset is zero")
	}
}

// --- OpenPR ------------------------------------------------------------------

func TestOpenPR(t *testing.T) {
	var prPayload map[string]string

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/org/repo-web/git/ref/heads/agent/issue-5":
			writeJSON(w, ghRef{Ref: "refs/heads/agent/issue-5"})

		case r.Method == http.MethodPost && r.URL.Path == "/repos/org/repo-web/pulls":
			if err := json.NewDecoder(r.Body).Decode(&prPayload); err != nil {
				t.Errorf("decode PR payload: %v", err)
			}
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, ghPRResponse{
				Number:  10,
				HTMLURL: "https://github.com/org/repo-web/pull/10",
				Title:   prPayload["title"],
				State:   "open",
			})

		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	p := newTestPoller(t, handler)
	pr, err := p.OpenPR(context.Background(), "web", OpenPRInput{
		Issue:  5,
		Branch: "agent/issue-5",
		Base:   "main",
		Title:  "feat: issue 5",
		Body:   "Closes #5\n\nImplements the feature.",
	})
	if err != nil {
		t.Fatalf("OpenPR: %v", err)
	}
	if pr.Number != 10 {
		t.Errorf("pr.Number = %d, want 10", pr.Number)
	}
	if pr.URL != "https://github.com/org/repo-web/pull/10" {
		t.Errorf("pr.URL = %q", pr.URL)
	}
	if pr.State != "open" {
		t.Errorf("pr.State = %q, want open", pr.State)
	}
	if prPayload["head"] != "agent/issue-5" {
		t.Errorf("head = %q, want agent/issue-5", prPayload["head"])
	}
	if prPayload["base"] != "main" {
		t.Errorf("base = %q, want main", prPayload["base"])
	}
}

func TestOpenPRBranchNotFound(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]string{"message": "Not Found"})
	})

	p := newTestPoller(t, handler)
	_, err := p.OpenPR(context.Background(), "web", OpenPRInput{
		Issue:  5,
		Branch: "nonexistent",
		Base:   "main",
		Title:  "PR title",
	})
	if err == nil {
		t.Fatal("expected error when branch not found")
	}
}

// --- Conditional ETag ---------------------------------------------------------

// TestListByLabelsETagSentAndReturned: 200 com ETag → envia If-None-Match na
// segunda chamada e retorna o novo ETag.
func TestListByLabelsETagSentAndReturned(t *testing.T) {
	const serverETag = `"abc123"`
	var receivedIfNoneMatch string

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedIfNoneMatch = r.Header.Get("If-None-Match")
		w.Header().Set("ETag", serverETag)
		writeJSON(w, []ghIssue{
			{Number: 1, Title: "feat", Labels: []ghLabel{{Name: "agent:ready"}}},
		})
	})

	p := newTestPoller(t, handler)
	// Primeira chamada sem ETag.
	issues, newETag, err := p.ListByLabels(context.Background(), "web", []string{"agent:ready"}, "")
	if err != nil {
		t.Fatalf("primeira ListByLabels: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("esperava 1 issue, got %d", len(issues))
	}
	if newETag != serverETag {
		t.Errorf("newETag = %q, want %q", newETag, serverETag)
	}
	if receivedIfNoneMatch != "" {
		t.Errorf("If-None-Match enviado na primeira chamada: %q", receivedIfNoneMatch)
	}

	// Segunda chamada com ETag recebido.
	receivedIfNoneMatch = ""
	_, _, _ = p.ListByLabels(context.Background(), "web", []string{"agent:ready"}, newETag)
	if receivedIfNoneMatch != serverETag {
		t.Errorf("If-None-Match = %q, want %q", receivedIfNoneMatch, serverETag)
	}
}

// TestListByLabels304: servidor responde 304 → retorna (nil, etag, nil).
func TestListByLabels304(t *testing.T) {
	const clientETag = `"xyz"`

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == clientETag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		writeJSON(w, []ghIssue{})
	})

	p := newTestPoller(t, handler)
	issues, retETag, err := p.ListByLabels(context.Background(), "web", []string{"agent:ready"}, clientETag)
	if err != nil {
		t.Fatalf("ListByLabels 304: %v", err)
	}
	if issues != nil {
		t.Errorf("esperava nil issues em 304, got %v", issues)
	}
	if retETag != clientETag {
		t.Errorf("ETag retornado = %q, want %q (original preservado)", retETag, clientETag)
	}
}
