// Package workspace gerencia os checkouts locais dos repos-alvo no disco
// persistente da VM (design.md §12). Cada repo lógico (web/mobile/hybrid) é
// clonado uma vez em <base_dir>/<repo> e mantido atualizado na branch base a
// cada despacho de tarefa, devolvendo o caminho absoluto que o TaskRunner usa
// como working dir do subprocesso `claude` e das operações de git.
package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/marciomacedo/argos/internal/config"
)

// Manager garante o checkout local de um repo-alvo.
type Manager interface {
	// Prepare garante o checkout do repo na base_dir (clone se ausente; senão
	// fetch + reset/checkout da branch base) e devolve o caminho ABSOLUTO.
	// Idempotente.
	Prepare(ctx context.Context, repo string) (string, error)
}

// Options configura o Manager.
type Options struct {
	// BaseDir é o diretório-raiz dos checkouts (<base_dir>/<repo>).
	BaseDir string
	// Repos é o mapa de repos lógicos → coordenadas no GitHub (owner/name).
	Repos map[string]config.RepoTarget
	// BaseBranch é a branch base mantida nos checkouts (ex.: "main").
	BaseBranch string
	// Host é o host de clone (ex.: "github.com").
	Host string
	// Token autentica o clone https (x-access-token). NUNCA é logado. Vazio
	// resulta numa URL sem credenciais (repos públicos / auth via helper).
	Token string
}

type manager struct {
	baseDir    string
	repos      map[string]config.RepoTarget
	baseBranch string
	host       string
	token      string
	git        gitClient
}

// New cria um Manager com as operações reais de git.
func New(opts Options) Manager {
	return newWithGit(opts, execGit{})
}

// newWithGit é o construtor interno: injeta o gitClient (fake nos testes).
func newWithGit(opts Options, git gitClient) *manager {
	host := opts.Host
	if host == "" {
		host = config.DefaultGitHost
	}
	branch := opts.BaseBranch
	if branch == "" {
		branch = config.DefaultBaseBranch
	}
	return &manager{
		baseDir:    opts.BaseDir,
		repos:      opts.Repos,
		baseBranch: branch,
		host:       host,
		token:      opts.Token,
		git:        git,
	}
}

// Prepare garante o checkout do repo e devolve o caminho absoluto.
func (m *manager) Prepare(ctx context.Context, repo string) (string, error) {
	rt, ok := m.repos[repo]
	if !ok {
		return "", fmt.Errorf("workspace: repo %q não configurado", repo)
	}

	path, err := filepath.Abs(filepath.Join(m.baseDir, repo))
	if err != nil {
		return "", fmt.Errorf("workspace: caminho absoluto de %q: %w", repo, err)
	}
	log := slog.With("repo", repo, "path", path)

	if isCheckout(path) {
		// Checkout existente: atualiza a branch base.
		if err := m.git.Fetch(ctx, path); err != nil {
			return "", fmt.Errorf("workspace: fetch %q: %w", repo, err)
		}
		if err := m.git.Checkout(ctx, path, m.baseBranch); err != nil {
			return "", fmt.Errorf("workspace: checkout %q em %q: %w", repo, m.baseBranch, err)
		}
		if err := m.git.ResetHard(ctx, path, "origin/"+m.baseBranch); err != nil {
			return "", fmt.Errorf("workspace: reset %q: %w", repo, err)
		}
		log.Info("workspace: checkout atualizado", "branch", m.baseBranch)
		return path, nil
	}

	// Sem checkout: garante o diretório-pai e clona.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("workspace: criar base_dir: %w", err)
	}
	if err := m.git.Clone(ctx, m.cloneURL(rt), path); err != nil {
		return "", fmt.Errorf("workspace: clone %q: %w", repo, err)
	}
	if err := m.git.Checkout(ctx, path, m.baseBranch); err != nil {
		return "", fmt.Errorf("workspace: checkout %q em %q: %w", repo, m.baseBranch, err)
	}
	log.Info("workspace: repo clonado", "branch", m.baseBranch)
	return path, nil
}

// cloneURL deriva a URL https de clone. O token (quando presente) é embutido
// como x-access-token; o valor NUNCA é logado.
func (m *manager) cloneURL(rt config.RepoTarget) string {
	if m.token != "" {
		return fmt.Sprintf("https://x-access-token:%s@%s/%s/%s.git", m.token, m.host, rt.Owner, rt.Name)
	}
	return fmt.Sprintf("https://%s/%s/%s.git", m.host, rt.Owner, rt.Name)
}

// isCheckout informa se path já contém um repositório git (dir .git presente).
func isCheckout(path string) bool {
	fi, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && fi.IsDir()
}
