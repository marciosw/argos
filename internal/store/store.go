// Package store é a camada de persistência do orquestrador sobre SQLite, via
// driver puro Go modernc.org/sqlite (sem CGO → CGO_ENABLED=0). Concentra o
// schema (migrations embutidas), as queries e o controle de estado operacional.
//
// Ver docs/specs/persistence.md.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound é retornado quando uma linha esperada não existe.
var ErrNotFound = errors.New("store: not found")

// tsLayout é o formato em que timestamps são gravados/lidos no SQLite
// (compatível com CURRENT_TIMESTAMP, sempre em UTC).
const tsLayout = "2006-01-02 15:04:05"

// Store encapsula o pool de conexões e as operações de persistência.
type Store struct {
	db  *sql.DB
	log *slog.Logger
}

// Options configura a abertura do Store.
type Options struct {
	// Path é o caminho do arquivo SQLite. Use ":memory:" para um banco em
	// memória (útil em testes).
	Path string
	// Logger opcional; se nil, usa slog.Default().
	Logger *slog.Logger
}

// Open abre (ou cria) o banco, aplica os PRAGMAs e roda as migrations.
func Open(ctx context.Context, opts Options) (*Store, error) {
	if opts.Path == "" {
		opts.Path = "./orchestrator.db"
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	db, err := sql.Open("sqlite", dsn(opts.Path))
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}

	// SQLite serializa escritas; mantemos um único escritor para evitar
	// SQLITE_BUSY e para que o banco em memória sobreviva entre chamadas
	// (database/sql descartaria um :memory: ao fechar a última conexão).
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}

	s := &Store{db: db, log: logger}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// dsn monta a URI do modernc.org/sqlite com os PRAGMAs aplicados por conexão.
// Ver persistence.md §2 (WAL, busy_timeout, foreign_keys).
func dsn(path string) string {
	mem := path == ":memory:" || path == ""
	if mem {
		// cache=shared + nome fixo: todas as conexões do pool veem o mesmo
		// banco em memória.
		path = "file::memory:"
	} else {
		path = "file:" + path
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(ON)")
	if mem {
		q.Set("cache", "shared")
	} else {
		// WAL só faz sentido para banco em arquivo.
		q.Add("_pragma", "journal_mode(WAL)")
	}
	return path + "?" + q.Encode()
}

// DB expõe o *sql.DB subjacente (uso avançado/testes).
func (s *Store) DB() *sql.DB { return s.db }

// Close fecha o pool de conexões.
func (s *Store) Close() error { return s.db.Close() }

// formatTS formata um time.Time no layout do banco (UTC).
func formatTS(t time.Time) string { return t.UTC().Format(tsLayout) }

// parseTS converte uma coluna de timestamp (TEXT) em time.Time. Retorna o
// zero-value quando nula/vazia/inválida.
func parseTS(ns sql.NullString) time.Time {
	if !ns.Valid || ns.String == "" {
		return time.Time{}
	}
	if t, err := time.Parse(tsLayout, ns.String); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(time.RFC3339, ns.String); err == nil {
		return t.UTC()
	}
	return time.Time{}
}
