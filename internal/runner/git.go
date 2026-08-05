package runner

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// gitOps abstrai as operações de git necessárias na fase de codificação, para
// testabilidade (fake nos testes; o `claude` real faz os commits, o runner
// garante a branch e empurra para o remoto antes de abrir o PR).
//
// Restrição do projeto: agentes só criam/empurram a branch agent/issue-{N};
// NUNCA push direto na base (main). Push força nunca é tentado (spec §10).
type gitOps interface {
	// EnsureBranch garante que o checkout em dir esteja na branch (criando-a a
	// partir do HEAD atual se necessário).
	EnsureBranch(ctx context.Context, dir, branch string) error
	// Push envia a branch para o remoto origin (com -u). Sem --force.
	Push(ctx context.Context, dir, branch string) error
	// CountCommits conta commits em base..branch (0 se não houver).
	CountCommits(ctx context.Context, dir, base, branch string) (int, error)
}

// execGit é a implementação real sobre o binário git.
type execGit struct{}

func (execGit) run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return out.String(), nil
}

func (g execGit) EnsureBranch(ctx context.Context, dir, branch string) error {
	// Já existe? Faz checkout. Senão, cria a partir do HEAD.
	if _, err := g.run(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		_, err := g.run(ctx, dir, "checkout", branch)
		return err
	}
	_, err := g.run(ctx, dir, "checkout", "-b", branch)
	return err
}

func (g execGit) Push(ctx context.Context, dir, branch string) error {
	_, err := g.run(ctx, dir, "push", "-u", "origin", branch)
	return err
}

func (g execGit) CountCommits(ctx context.Context, dir, base, branch string) (int, error) {
	out, err := g.run(ctx, dir, "rev-list", "--count", base+".."+branch)
	if err != nil {
		return 0, err
	}
	n, convErr := strconv.Atoi(strings.TrimSpace(out))
	if convErr != nil {
		return 0, fmt.Errorf("git rev-list count parse: %w", convErr)
	}
	return n, nil
}
