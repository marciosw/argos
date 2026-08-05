package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marciomacedo/argos/internal/config"
)

// fakeGit registra as chamadas e, no Clone, cria o diretório .git para que o
// próximo Prepare enxergue um checkout existente (idempotência).
type fakeGit struct {
	clones    []string // URLs clonadas
	fetched   []string // dirs com fetch
	checkouts []string // branches
	resets    []string // refs
}

func (f *fakeGit) Clone(_ context.Context, url, dir string) error {
	f.clones = append(f.clones, url)
	return os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
}
func (f *fakeGit) Fetch(_ context.Context, dir string) error {
	f.fetched = append(f.fetched, dir)
	return nil
}
func (f *fakeGit) Checkout(_ context.Context, _, branch string) error {
	f.checkouts = append(f.checkouts, branch)
	return nil
}
func (f *fakeGit) ResetHard(_ context.Context, _, ref string) error {
	f.resets = append(f.resets, ref)
	return nil
}

func testRepos() map[string]config.RepoTarget {
	return map[string]config.RepoTarget{
		"web": {Owner: "acme", Name: "repo-web"},
	}
}

func TestPrepareClonesWhenAbsent(t *testing.T) {
	base := t.TempDir()
	git := &fakeGit{}
	m := newWithGit(Options{
		BaseDir:    base,
		Repos:      testRepos(),
		BaseBranch: "main",
		Host:       "github.com",
		Token:      "secret-token",
	}, git)

	path, err := m.Prepare(context.Background(), "web")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	// Caminho absoluto e dentro da base_dir.
	if !filepath.IsAbs(path) {
		t.Errorf("caminho deveria ser absoluto: %q", path)
	}
	wantAbs, _ := filepath.Abs(filepath.Join(base, "web"))
	if path != wantAbs {
		t.Errorf("path esperado %q, got %q", wantAbs, path)
	}

	if len(git.clones) != 1 {
		t.Fatalf("esperava 1 clone, got %d", len(git.clones))
	}
	if len(git.fetched) != 0 {
		t.Errorf("não deveria fazer fetch em checkout ausente, got %v", git.fetched)
	}
	if len(git.checkouts) != 1 || git.checkouts[0] != "main" {
		t.Errorf("esperava checkout em main, got %v", git.checkouts)
	}
	// URL inclui token e coordenadas.
	url := git.clones[0]
	if !strings.Contains(url, "x-access-token:secret-token@github.com/acme/repo-web.git") {
		t.Errorf("URL de clone inesperada: %q", url)
	}
}

func TestPrepareFetchesWhenPresent(t *testing.T) {
	base := t.TempDir()
	// Simula checkout pré-existente.
	if err := os.MkdirAll(filepath.Join(base, "web", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	git := &fakeGit{}
	m := newWithGit(Options{BaseDir: base, Repos: testRepos(), BaseBranch: "main"}, git)

	if _, err := m.Prepare(context.Background(), "web"); err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	if len(git.clones) != 0 {
		t.Errorf("não deveria clonar checkout existente, got %v", git.clones)
	}
	if len(git.fetched) != 1 {
		t.Errorf("esperava 1 fetch, got %d", len(git.fetched))
	}
	if len(git.checkouts) != 1 || git.checkouts[0] != "main" {
		t.Errorf("esperava checkout em main, got %v", git.checkouts)
	}
	if len(git.resets) != 1 || git.resets[0] != "origin/main" {
		t.Errorf("esperava reset --hard origin/main, got %v", git.resets)
	}
}

func TestPrepareIdempotent(t *testing.T) {
	base := t.TempDir()
	git := &fakeGit{}
	m := newWithGit(Options{BaseDir: base, Repos: testRepos(), BaseBranch: "main"}, git)
	ctx := context.Background()

	p1, err := m.Prepare(ctx, "web")
	if err != nil {
		t.Fatalf("Prepare 1: %v", err)
	}
	p2, err := m.Prepare(ctx, "web")
	if err != nil {
		t.Fatalf("Prepare 2: %v", err)
	}
	if p1 != p2 {
		t.Errorf("caminhos divergem entre chamadas: %q vs %q", p1, p2)
	}
	// 1ª chamada clona; 2ª (já com .git criado pelo fakeGit) faz fetch.
	if len(git.clones) != 1 {
		t.Errorf("esperava exatamente 1 clone no total, got %d", len(git.clones))
	}
	if len(git.fetched) != 1 {
		t.Errorf("esperava 1 fetch na 2ª chamada, got %d", len(git.fetched))
	}
}

func TestPrepareUnknownRepo(t *testing.T) {
	m := newWithGit(Options{BaseDir: t.TempDir(), Repos: testRepos()}, &fakeGit{})
	if _, err := m.Prepare(context.Background(), "mobile"); err == nil {
		t.Fatal("esperava erro para repo não configurado")
	}
}

func TestCloneURLWithoutToken(t *testing.T) {
	m := newWithGit(Options{Repos: testRepos(), Host: "github.com"}, &fakeGit{})
	url := m.cloneURL(config.RepoTarget{Owner: "acme", Name: "repo-web"})
	if url != "https://github.com/acme/repo-web.git" {
		t.Errorf("URL sem token inesperada: %q", url)
	}
}
