package storage

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/georgysavva/scany/v2/sqlscan"
	"github.com/jtarchie/lqs"
	"github.com/jtarchie/pocketci/storage"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// Sqlite is the SQLite-backed storage driver. Method implementations are
// grouped by entity into sibling files (tasks.go, pipelines.go, runs.go,
// schedules.go, gates.go, webhook_dedup.go).
type Sqlite struct {
	writer    *sql.DB
	reader    *sql.DB
	namespace string
	tempFile  string // non-empty when we created a temp file for :memory: DSN
}

// Config holds the configuration for a SQLite storage driver.
type Config struct {
	Path string // database file path (use ":memory:" for in-memory)
}

func NewSqlite(cfg Config, namespace string, _ *slog.Logger) (storage.Driver, error) {
	dsn := cfg.Path

	// For in-memory databases, use a temp file so that both the reader and
	// writer connections share the same data. The file is removed when the
	// Sqlite instance is closed via a cleanup func stored on the struct.
	var tempFile string
	if dsn == ":memory:" {
		f, err := os.CreateTemp("", "pocketci-*.db")
		if err != nil {
			return nil, fmt.Errorf("failed to create temp db file: %w", err)
		}
		_ = f.Close()
		tempFile = f.Name()
		dsn = tempFile
	}

	writer, err := lqs.Open("sqlite", dsn, `
		PRAGMA journal_mode = WAL;
		PRAGMA synchronous = NORMAL;
		PRAGMA foreign_keys = ON;
		PRAGMA busy_timeout = 5000;
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	//nolint: noctx
	_, err = writer.Exec(schemaSQL)
	if err != nil {
		return nil, fmt.Errorf("failed to apply schema: %w", err)
	}

	err = applyMigrations(writer)
	if err != nil {
		return nil, fmt.Errorf("failed to apply migrations: %w", err)
	}

	writer.SetMaxIdleConns(1)
	writer.SetMaxOpenConns(1)

	reader, err := lqs.Open("sqlite", dsn, `
		PRAGMA foreign_keys = ON;
		PRAGMA busy_timeout = 5000;
		PRAGMA query_only = ON;
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	return &Sqlite{
		writer:    writer,
		reader:    reader,
		namespace: namespace,
		tempFile:  tempFile,
	}, nil
}

func (s *Sqlite) Close() error {
	err := s.writer.Close()
	if err != nil {
		return fmt.Errorf("failed to close database: %w", err)
	}

	err = s.reader.Close()
	if err != nil {
		return fmt.Errorf("failed to close database: %w", err)
	}

	if s.tempFile != "" {
		_ = os.Remove(s.tempFile)
	}

	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}

	return 0
}

// columnMigration describes an idempotent ALTER TABLE ADD COLUMN migration.
// Schema-level CREATE TABLE statements use IF NOT EXISTS so they don't add
// columns to a pre-existing table; these migrations close that gap by adding
// columns only when missing from the live schema.
type columnMigration struct {
	Table  string
	Column string
	Def    string
}

var columnMigrations = []columnMigration{
	{Table: "pipelines", Column: "concurrency_mode", Def: "TEXT NOT NULL DEFAULT ''"},
	{Table: "pipelines", Column: "concurrency_group_template", Def: "TEXT NOT NULL DEFAULT ''"},
	{Table: "pipelines", Column: "concurrency_cancel_running", Def: "INTEGER NOT NULL DEFAULT 0"},
	{Table: "pipeline_runs", Column: "concurrency_group", Def: "TEXT NOT NULL DEFAULT ''"},
}

func applyMigrations(db *sql.DB) error {
	for _, m := range columnMigrations {
		var exists int

		//nolint:noctx,execinquery // bootstrap path; sql.DB Query is fine here
		row := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`,
			m.Table, m.Column,
		)

		err := row.Scan(&exists)
		if err != nil {
			return fmt.Errorf("inspect %s.%s: %w", m.Table, m.Column, err)
		}

		if exists > 0 {
			continue
		}

		//nolint:noctx,gosec // table/column names are package-internal constants
		_, err = db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", m.Table, m.Column, m.Def))
		if err != nil {
			return fmt.Errorf("add column %s.%s: %w", m.Table, m.Column, err)
		}
	}

	// Ensure indexes added after the initial schema exist on upgraded databases.
	//nolint:noctx // bootstrap path
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_pipeline_runs_group_status
  ON pipeline_runs(concurrency_group, status) WHERE concurrency_group != ''`)
	if err != nil {
		return fmt.Errorf("create idx_pipeline_runs_group_status: %w", err)
	}

	return nil
}

// sanitizeFTSQuery converts a freeform user query into a safe FTS5 query.
// Each whitespace-separated token is treated as a literal prefix match term,
// preventing accidental use of FTS5 boolean operators (AND, OR, NOT, etc.).
func sanitizeFTSQuery(q string) string {
	words := strings.Fields(q)
	if len(words) == 0 {
		return ""
	}

	terms := make([]string, 0, len(words))

	for _, w := range words {
		// Escape any embedded double-quotes and wrap as a quoted literal with
		// prefix matching (*) so incremental search works naturally.
		safe := strings.ReplaceAll(w, `"`, `""`)
		terms = append(terms, `"`+safe+`"*`)
	}

	return strings.Join(terms, " ")
}

// paginatedSearch runs a paginated query with optional FTS filtering.
// When query is empty it uses countSQL/selectSQL; otherwise it sanitises the
// query and uses countFTSSQL/selectFTSSQL. The select SQL must end with
// ORDER BY ... (no LIMIT/OFFSET) — the helper appends LIMIT ? OFFSET ?.
// baseArgs are prepended to every query; ftsQuery is appended for FTS queries.
func paginatedSearch[S any, T any](
	ctx context.Context,
	db *sql.DB,
	page, perPage int,
	query string,
	countSQL, selectSQL string,
	countFTSSQL, selectFTSSQL string,
	baseArgs []any,
	convert func(S) T,
) (*storage.PaginationResult[T], error) {
	if page < 1 {
		page = 1
	}

	if perPage < 1 {
		perPage = 20
	}

	offset := (page - 1) * perPage

	var (
		cntSQL string
		selSQL string
		args   []any
	)

	if query == "" {
		cntSQL = countSQL
		selSQL = selectSQL + "\n\t\t\tLIMIT ? OFFSET ?"
		args = append(args, baseArgs...)
	} else {
		ftsQuery := sanitizeFTSQuery(query)
		cntSQL = countFTSSQL
		selSQL = selectFTSSQL + "\n\t\t\tLIMIT ? OFFSET ?"
		args = append(args, baseArgs...)
		args = append(args, ftsQuery)
	}

	var totalItems int
	err := sqlscan.Get(ctx, db, &totalItems, cntSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to count results: %w", err)
	}

	selectArgs := append(args, perPage, offset) //nolint:gocritic // intentional new slice

	var rows []S
	err = sqlscan.Select(ctx, db, &rows, selSQL, selectArgs...)
	if err != nil {
		return nil, fmt.Errorf("failed to query results: %w", err)
	}

	items := make([]T, 0, len(rows))
	for _, row := range rows {
		items = append(items, convert(row))
	}

	totalPages := (totalItems + perPage - 1) / perPage

	return &storage.PaginationResult[T]{
		Items:      items,
		Page:       page,
		PerPage:    perPage,
		TotalItems: totalItems,
		TotalPages: totalPages,
		HasNext:    page < totalPages,
	}, nil
}
