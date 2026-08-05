// Package migrations expõe os arquivos .sql de migração embutidos no binário,
// para que sejam aplicados no startup sem depender do filesystem de deploy.
package migrations

import "embed"

// FS contém todos os arquivos de migração (`NNNN_*.sql`), aplicados em ordem
// lexicográfica pelo pacote internal/store.
//
//go:embed *.sql
var FS embed.FS
