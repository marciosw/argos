package preview

import (
	"fmt"
	"sync"

	"github.com/marciomacedo/argos/internal/config"
	"github.com/marciomacedo/argos/internal/domain"
)

// PortManager aloca e libera portas no range configurado para servidores de dev.
// Thread-safe.
type PortManager struct {
	start, end int
	inUse      map[int]struct{}
	mu         sync.Mutex
}

// NewPortManager cria um PortManager com o range de portas da config.
func NewPortManager(cfg config.PreviewConfig) *PortManager {
	return &PortManager{
		start: cfg.PortRangeStart,
		end:   cfg.PortRangeEnd,
		inUse: make(map[int]struct{}),
	}
}

// Alloc aloca a primeira porta livre no range. Para o repo web, reserva
// duas portas consecutivas (port, port+1); para os demais, reserva uma
// (extraPort retorna 0). Retorna erro se não houver porta(s) disponível(is).
func (p *PortManager) Alloc(repo string) (port, extraPort int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	needTwo := repo == domain.RepoWeb

	for i := p.start; i <= p.end; i++ {
		if _, used := p.inUse[i]; used {
			continue
		}
		if needTwo {
			next := i + 1
			if next > p.end {
				break
			}
			if _, used := p.inUse[next]; used {
				continue
			}
			p.inUse[i] = struct{}{}
			p.inUse[next] = struct{}{}
			return i, next, nil
		}
		p.inUse[i] = struct{}{}
		return i, 0, nil
	}

	if needTwo {
		return 0, 0, fmt.Errorf("preview: nenhum par de portas consecutivas livre em [%d, %d]", p.start, p.end)
	}
	return 0, 0, fmt.Errorf("preview: nenhuma porta livre em [%d, %d]", p.start, p.end)
}

// Release libera as portas previamente alocadas. extraPort 0 é ignorado.
func (p *PortManager) Release(port, extraPort int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.inUse, port)
	if extraPort != 0 {
		delete(p.inUse, extraPort)
	}
}
