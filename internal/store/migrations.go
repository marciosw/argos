package store

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/marciomacedo/argos/migrations"
)

// migrate aplica, em ordem, todas as migrations embutidas ainda não aplicadas.
// A tabela schema_migrations controla a versão corrente (ver persistence.md §7).
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	files, err := loadMigrations()
	if err != nil {
		return err
	}

	for _, m := range files {
		var exists int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM schema_migrations WHERE version = ?`, m.version,
		).Scan(&exists); err != nil {
			return fmt.Errorf("store: check migration %d: %w", m.version, err)
		}
		if exists > 0 {
			continue
		}

		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("store: begin migration %d: %w", m.version, err)
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: apply migration %d (%s): %w", m.version, m.name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version) VALUES (?)`, m.version,
		); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: record migration %d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: commit migration %d: %w", m.version, err)
		}
		s.log.Info("migration applied", "version", m.version, "name", m.name)
	}
	return nil
}

type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations lê os arquivos `NNNN_*.sql` embutidos, ordenados por versão.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("store: read migrations: %w", err)
	}

	var out []migration
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			return nil, fmt.Errorf("store: migration %q sem prefixo de versão (NNNN_*.sql)", name)
		}
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("store: migration %q com versão inválida: %w", name, err)
		}
		data, err := migrations.FS.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("store: read migration %q: %w", name, err)
		}
		out = append(out, migration{version: version, name: name, sql: string(data)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}
