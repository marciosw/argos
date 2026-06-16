package github

import (
	"context"
	"fmt"
	"log/slog"
)

// ghRef é a resposta de GET /repos/{o}/{r}/git/ref/heads/{branch}.
type ghRef struct {
	Ref    string `json:"ref"`
	Object struct {
		SHA string `json:"sha"`
	} `json:"object"`
}

// ghPRResponse é a resposta de POST /repos/{o}/{r}/pulls.
type ghPRResponse struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	Title   string `json:"title"`
	State   string `json:"state"`
}

// OpenPR confirma que a branch existe no remoto e abre o Pull Request.
func (p *poller) OpenPR(ctx context.Context, repo string, in OpenPRInput) (PullRequest, error) {
	base, err := p.repoBase(repo)
	if err != nil {
		return PullRequest{}, err
	}

	// 1. Confirma que a branch existe no remoto.
	refPath := fmt.Sprintf("%s/git/ref/heads/%s", base, in.Branch)
	var ref ghRef
	if _, err := p.client.do(ctx, "GET", refPath, nil, &ref); err != nil {
		return PullRequest{}, fmt.Errorf("github: branch %q não encontrada em %s: %w", in.Branch, repo, err)
	}

	// 2. Abre o Pull Request.
	payload := map[string]string{
		"title": in.Title,
		"head":  in.Branch,
		"base":  in.Base,
		"body":  in.Body,
	}
	var pr ghPRResponse
	if _, err := p.client.do(ctx, "POST", base+"/pulls", payload, &pr); err != nil {
		return PullRequest{}, fmt.Errorf("github: OpenPR %s issue#%d: %w", repo, in.Issue, err)
	}

	slog.Info("github: PR aberto", "repo", repo, "issue", in.Issue, "pr_number", pr.Number, "url", pr.HTMLURL)
	return PullRequest{
		Number: pr.Number,
		URL:    pr.HTMLURL,
		Title:  pr.Title,
		State:  pr.State,
	}, nil
}
