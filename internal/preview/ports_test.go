package preview

import (
	"testing"

	"github.com/marciomacedo/argos/internal/config"
	"github.com/marciomacedo/argos/internal/domain"
)

func TestPortManagerNonWeb(t *testing.T) {
	cfg := config.PreviewConfig{PortRangeStart: 9000, PortRangeEnd: 9002}
	pm := NewPortManager(cfg)

	// Range [9000, 9002] = 3 portas para repos não-web.
	p1, e1, err := pm.Alloc(domain.RepoMobile)
	if err != nil || p1 != 9000 || e1 != 0 {
		t.Fatalf("alloc 1: port=%d extra=%d err=%v", p1, e1, err)
	}
	p2, _, err := pm.Alloc(domain.RepoMobile)
	if err != nil || p2 != 9001 {
		t.Fatalf("alloc 2: port=%d err=%v", p2, err)
	}
	p3, _, err := pm.Alloc(domain.RepoHybrid)
	if err != nil || p3 != 9002 {
		t.Fatalf("alloc 3: port=%d err=%v", p3, err)
	}

	// Todas usadas — deve falhar.
	if _, _, err := pm.Alloc(domain.RepoMobile); err == nil {
		t.Fatal("quero erro quando range esgotado")
	}

	// Liberar p2 e realocar.
	pm.Release(p2, 0)
	p4, _, err := pm.Alloc(domain.RepoHybrid)
	if err != nil || p4 != p2 {
		t.Fatalf("alloc após release: port=%d err=%v", p4, err)
	}
}

func TestPortManagerWeb(t *testing.T) {
	cfg := config.PreviewConfig{PortRangeStart: 9000, PortRangeEnd: 9003}
	pm := NewPortManager(cfg)

	// Range [9000, 9003] = 2 pares consecutivos para web.
	p1, e1, err := pm.Alloc(domain.RepoWeb)
	if err != nil || p1 != 9000 || e1 != 9001 {
		t.Fatalf("alloc web 1: port=%d extra=%d err=%v", p1, e1, err)
	}
	p2, e2, err := pm.Alloc(domain.RepoWeb)
	if err != nil || p2 != 9002 || e2 != 9003 {
		t.Fatalf("alloc web 2: port=%d extra=%d err=%v", p2, e2, err)
	}

	// Sem par disponível.
	if _, _, err := pm.Alloc(domain.RepoWeb); err == nil {
		t.Fatal("quero erro quando não há par livre")
	}

	// Liberar o primeiro par e realocar.
	pm.Release(p1, e1)
	p3, e3, err := pm.Alloc(domain.RepoWeb)
	if err != nil || p3 != 9000 || e3 != 9001 {
		t.Fatalf("alloc web após release: port=%d extra=%d err=%v", p3, e3, err)
	}
}

func TestPortManagerMixedAlloc(t *testing.T) {
	cfg := config.PreviewConfig{PortRangeStart: 9000, PortRangeEnd: 9005}
	pm := NewPortManager(cfg)

	// Aloca não-web na 9000.
	p, _, _ := pm.Alloc(domain.RepoMobile)
	if p != 9000 {
		t.Fatalf("esperava 9000, veio %d", p)
	}

	// Web deve pular 9000 (em uso) e pegar 9001+9002.
	wp, we, err := pm.Alloc(domain.RepoWeb)
	if err != nil || wp != 9001 || we != 9002 {
		t.Fatalf("alloc web com buraco: port=%d extra=%d err=%v", wp, we, err)
	}
}

func TestPortManagerReleaseNoop(t *testing.T) {
	cfg := config.PreviewConfig{PortRangeStart: 9000, PortRangeEnd: 9010}
	pm := NewPortManager(cfg)
	// Release de porta não alocada não deve entrar em pânico.
	pm.Release(9005, 0)
	pm.Release(9005, 9006)
}
