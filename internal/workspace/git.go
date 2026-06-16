package workspace

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// gitClient abstrai as operações de git usadas pelo workspace, para permitir
// testes sem rede nem binário git real (fake nos testes). A implementação real
// é execGit.
//
// As credenciais (token embutido na URL de clone) NUNCA são logadas por este
// pacote: o Manager loga apenas repo lógico e caminho local.
type gitClient interface {
	// Clone clona url em dir (que ainda não existe).
	Clone(ctx context.Context, url, dir string) error
	// Fetch atualiza as refs do remoto origin no checkout em dir.
	Fetch(ctx context.Context, dir string) error
	// Checkout posiciona o checkout em dir na branch indicada.
	Checkout(ctx context.Context, dir, branch string) error
	// ResetHard reposiciona a árvore de trabalho em ref (ex.: origin/main).
	ResetHard(ctx context.Context, dir, ref string) error
}

// execGit é a implementação real sobre o binário git.
type execGit struct{}

func (execGit) run(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

func (g execGit) Clone(ctx context.Context, url, dir string) error {
	return g.run(ctx, "", "clone", url, dir)
}

func (g execGit) Fetch(ctx context.Context, dir string) error {
	return g.run(ctx, dir, "fetch", "--prune", "origin")
}

func (g execGit) Checkout(ctx context.Context, dir, branch string) error {
	return g.run(ctx, dir, "checkout", branch)
}

func (g execGit) ResetHard(ctx context.Context, dir, ref string) error {
	return g.run(ctx, dir, "reset", "--hard", ref)
}
