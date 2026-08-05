package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/marciomacedo/argos/internal/config"
)

// Issue é a representação bruta de uma issue do GitHub (com labels e URL).
// Diferente de domain.Issue, que contém o estado operacional no SQLite.
type Issue struct {
	Repo      string
	Number    int
	Title     string
	Body      string
	Labels    []string
	UpdatedAt time.Time
	URL       string
}

// OpenPRInput é a entrada para OpenPR.
type OpenPRInput struct {
	Issue  int
	Branch string // ex.: agent/issue-42
	Base   string // ex.: main
	Title  string
	Body   string // deve incluir "Closes #N"
}

// PullRequest é o PR retornado pelo GitHub ao criar via API.
type PullRequest struct {
	Number int
	URL    string
	Title  string
	State  string
}

// Poller executa as operações de GitHub determinadas pelo Scheduler.
// Não decide transições de negócio (ver github_poller.md §1).
type Poller interface {
	// ListByLabels lista issues abertas com qualquer das labels (OR), usando
	// requisição condicional se etag != "". Retorna (nil, etag, nil) em 304
	// (sem mudanças); retorna (issues, newETag, nil) em 200.
	ListByLabels(ctx context.Context, repo string, labels []string, etag string) ([]Issue, string, error)
	GetIssue(ctx context.Context, repo string, issue int) (Issue, error)
	TransitionLabel(ctx context.Context, repo string, issue int, fromLabel, toLabel string) error
	SetLabel(ctx context.Context, repo string, issue int, label string) error
	ClearLabel(ctx context.Context, repo string, issue int, label string) error
	Comment(ctx context.Context, repo string, issue int, body string) error
	OpenPR(ctx context.Context, repo string, in OpenPRInput) (PullRequest, error)
}

type poller struct {
	client *Client
	repos  map[string]config.RepoTarget
}

// NewPoller cria um Poller com o client e o mapa de repositórios da config.
func NewPoller(client *Client, repos map[string]config.RepoTarget) Poller {
	return &poller{client: client, repos: repos}
}

// NewPollerFromConfig lê o token da env nomeada em cfg.TokenEnv e cria Client + Poller.
// Retorna erro se a env var não estiver definida.
func NewPollerFromConfig(cfg config.GitHubConfig) (Poller, error) {
	token := os.Getenv(cfg.TokenEnv)
	if token == "" {
		return nil, fmt.Errorf("github: env var %q não definida (github.token_env)", cfg.TokenEnv)
	}
	return NewPoller(NewClient(cfg.APIBase, token), cfg.Repos), nil
}

// --- helpers -----------------------------------------------------------------

func (p *poller) repoBase(repo string) (string, error) {
	rt, ok := p.repos[repo]
	if !ok {
		return "", fmt.Errorf("github: repo %q não configurado", repo)
	}
	return fmt.Sprintf("/repos/%s/%s", rt.Owner, rt.Name), nil
}

// ghIssue é o JSON de uma issue retornada pela API do GitHub.
type ghIssue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Labels    []ghLabel `json:"labels"`
	UpdatedAt time.Time `json:"updated_at"`
	HTMLURL   string    `json:"html_url"`
}

type ghLabel struct {
	Name string `json:"name"`
}

// --- Poller ------------------------------------------------------------------

// ListByLabels lista issues abertas com qualquer das labels (OR).
// Se etag != "", envia If-None-Match e retorna (nil, etag, nil) em 304.
// Em 200 retorna (issues, newETag, nil).
func (p *poller) ListByLabels(ctx context.Context, repo string, labels []string, etag string) ([]Issue, string, error) {
	base, err := p.repoBase(repo)
	if err != nil {
		return nil, "", err
	}
	q := url.Values{
		"state":    {"open"},
		"labels":   {strings.Join(labels, ",")},
		"per_page": {"100"},
	}
	var raw []ghIssue
	_, newETag, err := p.client.doConditional(ctx, base+"/issues?"+q.Encode(), etag, &raw)
	if err != nil {
		if errors.Is(err, ErrNotModified) {
			slog.Debug("github: ListByLabels 304 not modified", "repo", repo, "etag", etag)
			return nil, etag, nil
		}
		return nil, "", fmt.Errorf("github: ListByLabels %s: %w", repo, err)
	}
	out := make([]Issue, 0, len(raw))
	for _, r := range raw {
		lbls := make([]string, len(r.Labels))
		for i, l := range r.Labels {
			lbls[i] = l.Name
		}
		out = append(out, Issue{
			Repo:      repo,
			Number:    r.Number,
			Title:     r.Title,
			Body:      r.Body,
			Labels:    lbls,
			UpdatedAt: r.UpdatedAt,
			URL:       r.HTMLURL,
		})
	}
	slog.Debug("github: ListByLabels", "repo", repo, "labels", labels, "found", len(out), "etag", newETag)
	return out, newETag, nil
}

// GetIssue busca uma issue específica (inclui o corpo/Body, usado pelo
// TaskRunner para semear context.md).
func (p *poller) GetIssue(ctx context.Context, repo string, issue int) (Issue, error) {
	base, err := p.repoBase(repo)
	if err != nil {
		return Issue{}, err
	}
	var r ghIssue
	if _, err := p.client.do(ctx, "GET", fmt.Sprintf("%s/issues/%d", base, issue), nil, &r); err != nil {
		return Issue{}, fmt.Errorf("github: GetIssue %s#%d: %w", repo, issue, err)
	}
	lbls := make([]string, len(r.Labels))
	for i, l := range r.Labels {
		lbls[i] = l.Name
	}
	return Issue{
		Repo:      repo,
		Number:    r.Number,
		Title:     r.Title,
		Body:      r.Body,
		Labels:    lbls,
		UpdatedAt: r.UpdatedAt,
		URL:       r.HTMLURL,
	}, nil
}

// TransitionLabel remove fromLabel e adiciona toLabel de forma reconciliada:
// lê labels atuais, calcula diff e aplica via PUT (substituição total/atômica).
func (p *poller) TransitionLabel(ctx context.Context, repo string, issue int, fromLabel, toLabel string) error {
	current, err := p.getLabels(ctx, repo, issue)
	if err != nil {
		return err
	}
	next := computeTransition(current, fromLabel, toLabel)
	if setEq(current, next) {
		slog.Debug("github: TransitionLabel noop", "repo", repo, "issue", issue, "from", fromLabel, "to", toLabel)
		return nil
	}
	slog.Info("github: TransitionLabel", "repo", repo, "issue", issue, "from", fromLabel, "to", toLabel, "labels", next)
	return p.putLabels(ctx, repo, issue, next)
}

// SetLabel adiciona label à issue (idempotente via reconciliação).
func (p *poller) SetLabel(ctx context.Context, repo string, issue int, label string) error {
	current, err := p.getLabels(ctx, repo, issue)
	if err != nil {
		return err
	}
	if contains(current, label) {
		return nil
	}
	return p.putLabels(ctx, repo, issue, append(current, label))
}

// ClearLabel remove label da issue (idempotente via reconciliação).
func (p *poller) ClearLabel(ctx context.Context, repo string, issue int, label string) error {
	current, err := p.getLabels(ctx, repo, issue)
	if err != nil {
		return err
	}
	if !contains(current, label) {
		return nil
	}
	next := make([]string, 0, len(current)-1)
	for _, l := range current {
		if l != label {
			next = append(next, l)
		}
	}
	return p.putLabels(ctx, repo, issue, next)
}

// Comment cria um comentário de texto na issue.
func (p *poller) Comment(ctx context.Context, repo string, issue int, body string) error {
	base, err := p.repoBase(repo)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("%s/issues/%d/comments", base, issue)
	payload := map[string]string{"body": body}
	if _, err := p.client.do(ctx, "POST", path, payload, nil); err != nil {
		return fmt.Errorf("github: Comment %s#%d: %w", repo, issue, err)
	}
	slog.Info("github: Comment criado", "repo", repo, "issue", issue)
	return nil
}

// --- label primitives --------------------------------------------------------

func (p *poller) getLabels(ctx context.Context, repo string, issue int) ([]string, error) {
	base, err := p.repoBase(repo)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("%s/issues/%d/labels", base, issue)
	var raw []ghLabel
	if _, err := p.client.do(ctx, "GET", path, nil, &raw); err != nil {
		return nil, fmt.Errorf("github: getLabels %s#%d: %w", repo, issue, err)
	}
	out := make([]string, len(raw))
	for i, l := range raw {
		out[i] = l.Name
	}
	return out, nil
}

func (p *poller) putLabels(ctx context.Context, repo string, issue int, labels []string) error {
	base, err := p.repoBase(repo)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("%s/issues/%d/labels", base, issue)
	payload := map[string][]string{"labels": labels}
	if _, err := p.client.do(ctx, "PUT", path, payload, nil); err != nil {
		return fmt.Errorf("github: putLabels %s#%d: %w", repo, issue, err)
	}
	return nil
}

// computeTransition calcula os labels após remover fromLabel e adicionar toLabel.
func computeTransition(current []string, fromLabel, toLabel string) []string {
	next := make([]string, 0, len(current))
	for _, l := range current {
		if l != fromLabel {
			next = append(next, l)
		}
	}
	if !contains(next, toLabel) {
		next = append(next, toLabel)
	}
	return next
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// setEq retorna true se a e b contêm os mesmos elementos (sem considerar ordem).
func setEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]struct{}, len(a))
	for _, v := range a {
		m[v] = struct{}{}
	}
	for _, v := range b {
		if _, ok := m[v]; !ok {
			return false
		}
	}
	return true
}
