package storage

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/georgysavva/scany/v2/sqlscan"
	"github.com/jtarchie/lqs"
	"github.com/jtarchie/pocketci/runtime/support"
	"github.com/jtarchie/pocketci/storage"
	"github.com/samber/lo"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

type Sqlite struct {
	writer    *sql.DB
	reader    *sql.DB
	namespace string
	tempFile  string // non-empty when we created a temp file for :memory: DSN
}

// pipelineScan is an intermediate struct for scanning pipeline rows.
// Timestamps are stored as INTEGER (Unix epoch seconds).
type pipelineScan struct {
	ID             string `db:"id"`
	Name           string `db:"name"`
	Content        string `db:"content"`
	ContentType    string `db:"content_type"`
	Driver         string `db:"driver"`
	ResumeEnabled  int    `db:"resume_enabled"`
	RBACExpression string `db:"rbac_expression"`
	CreatedAt      int64  `db:"created_at"`
	UpdatedAt      int64  `db:"updated_at"`
}

func (p pipelineScan) toStorage() storage.Pipeline {
	return storage.Pipeline{
		ID:             p.ID,
		Name:           p.Name,
		Content:        p.Content,
		ContentType:    p.ContentType,
		Driver:         p.Driver,
		ResumeEnabled:  p.ResumeEnabled != 0,
		RBACExpression: p.RBACExpression,
		CreatedAt:      time.Unix(p.CreatedAt, 0).UTC(),
		UpdatedAt:      time.Unix(p.UpdatedAt, 0).UTC(),
	}
}

// pipelineRunScan is an intermediate struct for scanning pipeline run rows.
// Timestamps are stored as INTEGER (Unix epoch seconds); nullable timestamps use sql.NullInt64.
type pipelineRunScan struct {
	ID           string         `db:"id"`
	PipelineID   string         `db:"pipeline_id"`
	Status       string         `db:"status"`
	StartedAt    sql.NullInt64  `db:"started_at"`
	CompletedAt  sql.NullInt64  `db:"completed_at"`
	ErrorMessage sql.NullString `db:"error_message"`
	CreatedAt    int64          `db:"created_at"`
}

func (p pipelineRunScan) toStorage() storage.PipelineRun {
	run := storage.PipelineRun{
		ID:         p.ID,
		PipelineID: p.PipelineID,
		Status:     storage.RunStatus(p.Status),
		CreatedAt:  time.Unix(p.CreatedAt, 0).UTC(),
	}

	if p.StartedAt.Valid {
		t := time.Unix(p.StartedAt.Int64, 0).UTC()
		run.StartedAt = &t
	}

	if p.CompletedAt.Valid {
		t := time.Unix(p.CompletedAt.Int64, 0).UTC()
		run.CompletedAt = &t
	}

	if p.ErrorMessage.Valid {
		run.ErrorMessage = p.ErrorMessage.String
	}

	return run
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

// extractRunID returns the run ID from a task path if the path contains a
// "pipeline" segment (e.g. /ns/pipeline/{run_id}/steps/...). Returns "" otherwise.
func extractRunID(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if p == "pipeline" && i+1 < len(parts) {
			return parts[i+1]
		}
	}

	return ""
}

func (s *Sqlite) Set(ctx context.Context, prefix string, payload any) error {
	path := filepath.Clean("/" + s.namespace + "/" + prefix)
	runID := extractRunID(path)

	contents, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	_, err = s.writer.ExecContext(ctx, `
		INSERT INTO tasks (path, run_id, status, started_at, elapsed, error_message, error_type, payload)
		VALUES (
			?,
			NULLIF(?, ''),
			json_extract(?, '$.status'),
			json_extract(?, '$.started_at'),
			json_extract(?, '$.elapsed'),
			json_extract(?, '$.error_message'),
			json_extract(?, '$.error_type'),
			jsonb(?)
		)
		ON CONFLICT(path) DO UPDATE SET
			status        = COALESCE(json_extract(json(excluded.payload), '$.status'),        tasks.status),
			started_at    = COALESCE(json_extract(json(excluded.payload), '$.started_at'),    tasks.started_at),
			elapsed       = COALESCE(json_extract(json(excluded.payload), '$.elapsed'),       tasks.elapsed),
			error_message = COALESCE(json_extract(json(excluded.payload), '$.error_message'), tasks.error_message),
			error_type    = COALESCE(json_extract(json(excluded.payload), '$.error_type'),    tasks.error_type),
			payload       = jsonb_patch(tasks.payload, excluded.payload);
	`, path, runID, contents, contents, contents, contents, contents, contents)
	if err != nil {
		return fmt.Errorf("failed to insert task: %w", err)
	}

	// Keep the FTS index in sync: delete any stale entry then insert fresh.
	_, err = s.writer.ExecContext(ctx, `DELETE FROM data_fts WHERE path = ?`, path)
	if err != nil {
		return fmt.Errorf("failed to clear data_fts: %w", err)
	}

	text := StripANSI(extractTextFromJSON(contents))
	_, err = s.writer.ExecContext(ctx, `INSERT INTO data_fts(path, content) VALUES (?, ?)`, path, path+" "+text)
	if err != nil {
		return fmt.Errorf("failed to index data_fts: %w", err)
	}

	return nil
}

func (s *Sqlite) Get(ctx context.Context, prefix string) (storage.Payload, error) {
	path := filepath.Clean("/" + s.namespace + "/" + prefix)

	var payload storage.Payload
	var payloadBytes []byte

	err := sqlscan.Get(ctx, s.reader, &payloadBytes, `
		SELECT json(payload) FROM tasks WHERE path = ?
	`, path)
	if err != nil {
		if sqlscan.NotFound(err) {
			return nil, storage.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get task: %w", err)
	}

	err = json.Unmarshal(payloadBytes, &payload)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal payload: %w", err)
	}

	return payload, nil
}

func (s *Sqlite) GetAll(ctx context.Context, prefix string, fields []string) (storage.Results, error) {
	if len(fields) == 0 {
		fields = []string{"status"}
	}

	path := filepath.Clean("/" + s.namespace + "/" + prefix)

	var results storage.Results

	// Support wildcard "*" to return all fields
	var query string
	if len(fields) == 1 && fields[0] == "*" {
		query = `
			SELECT
				id, path, json(payload) as payload
			FROM
				tasks
			WHERE path GLOB :path
			ORDER BY
				id ASC
		`
	} else {
		// Fields that have been promoted to real columns — read directly instead of
		// computing json_extract(payload, '$.field') on every row.
		knownColumns := map[string]struct{}{
			"status": {}, "started_at": {}, "elapsed": {}, "error_message": {}, "error_type": {},
		}

		jsonSelects := strings.Join(
			lo.Map(fields, func(field string, _ int) string {
				if _, ok := knownColumns[field]; ok {
					return fmt.Sprintf("'%s', %s", field, field)
				}

				return fmt.Sprintf("'%s', json_extract(payload, '$.%s')", field, field)
			}),
			",",
		)

		query = fmt.Sprintf(`
			SELECT
				id, path, json_object(%s) as payload
			FROM
				tasks
			WHERE path GLOB :path
			ORDER BY
				id ASC
		`, jsonSelects)
	}

	err := sqlscan.Select(
		ctx,
		s.reader,
		&results,
		query,
		sql.Named("path", path+"*"),
	)
	if err != nil {
		return nil, fmt.Errorf("could not select: %w", err)
	}

	return results, nil
}

func (s *Sqlite) UpdateStatusForPrefix(ctx context.Context, prefix string, matchStatuses []string, newStatus string) error {
	if len(matchStatuses) == 0 {
		return nil
	}

	path := filepath.Clean("/" + s.namespace + "/" + prefix)

	placeholders := strings.Repeat("?,", len(matchStatuses))
	placeholders = placeholders[:len(placeholders)-1]

	query := fmt.Sprintf(
		`UPDATE tasks
		 SET   status  = ?,
		       payload = jsonb_patch(payload, json_object('status', ?))
		 WHERE status IN (%s) AND path GLOB ?`,
		placeholders,
	)

	args := make([]any, 0, 2+len(matchStatuses)+1)
	args = append(args, newStatus, newStatus)
	for _, status := range matchStatuses {
		args = append(args, status)
	}
	args = append(args, path+"*")

	_, err := s.writer.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to update task statuses for prefix %q: %w", prefix, err)
	}

	return nil
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

// SavePipeline creates or updates a pipeline in the database.
// Pipeline names are unique; saving with an existing name updates the record
// while preserving the original ID so existing pipeline_runs references remain valid.
func (s *Sqlite) SavePipeline(ctx context.Context, name, content, driver, contentType string) (*storage.Pipeline, error) {
	newID := support.PipelineID(name, content)
	now := time.Now().UTC()

	// Look up any existing pipeline by name so we can preserve its ID and clean up FTS.
	var existingID string
	_ = sqlscan.Get(ctx, s.writer, &existingID, `SELECT id FROM pipelines WHERE name = ?`, name)

	_, err := s.writer.ExecContext(ctx, `
		INSERT INTO pipelines (id, name, content, content_type, driver, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			content=excluded.content,
			content_type=excluded.content_type,
			driver=excluded.driver,
			updated_at=excluded.updated_at
	`, newID, name, content, contentType, driver, now.Unix(), now.Unix())
	if err != nil {
		return nil, fmt.Errorf("failed to save pipeline: %w", err)
	}

	// The stored ID is the pre-existing one (if updating) or the new hash (if inserting).
	storedID := newID
	if existingID != "" {
		storedID = existingID
	}

	// Keep FTS index in sync: delete any existing entry then re-insert.
	_, err = s.writer.ExecContext(ctx, `
		DELETE FROM pipelines_fts WHERE rowid IN (
			SELECT rowid FROM pipelines_fts WHERE id = ?
		)
	`, storedID)
	if err != nil {
		return nil, fmt.Errorf("failed to clear pipelines_fts: %w", err)
	}

	_, err = s.writer.ExecContext(ctx,
		`INSERT INTO pipelines_fts(id, name, content) VALUES (?, ?, ?)`,
		storedID, name, content,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to index pipeline: %w", err)
	}

	return &storage.Pipeline{
		ID:          storedID,
		Name:        name,
		Content:     content,
		ContentType: contentType,
		Driver:      driver,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// GetPipeline retrieves a pipeline by its ID.
func (s *Sqlite) GetPipeline(ctx context.Context, id string) (*storage.Pipeline, error) {
	var row pipelineScan

	err := sqlscan.Get(ctx, s.writer, &row, `
		SELECT id, name, content, content_type, driver, resume_enabled, rbac_expression, created_at, updated_at
		FROM pipelines WHERE id = ?
	`, id)
	if err != nil {
		if sqlscan.NotFound(err) {
			return nil, storage.ErrNotFound
		}

		return nil, fmt.Errorf("failed to get pipeline: %w", err)
	}

	p := row.toStorage()

	return &p, nil
}

// GetPipelineByName retrieves the most recently updated pipeline with the given name.
func (s *Sqlite) GetPipelineByName(ctx context.Context, name string) (*storage.Pipeline, error) {
	var row pipelineScan

	err := sqlscan.Get(ctx, s.writer, &row, `
		SELECT id, name, content, content_type, driver, resume_enabled, rbac_expression, created_at, updated_at
		FROM pipelines WHERE name = ?
		ORDER BY updated_at DESC LIMIT 1
	`, name)
	if err != nil {
		if sqlscan.NotFound(err) {
			return nil, storage.ErrNotFound
		}

		return nil, fmt.Errorf("failed to get pipeline by name: %w", err)
	}

	p := row.toStorage()

	return &p, nil
}

// DeletePipeline removes a pipeline by its ID.
func (s *Sqlite) DeletePipeline(ctx context.Context, id string) error {
	result, err := s.writer.ExecContext(ctx, `DELETE FROM pipelines WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete pipeline: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return storage.ErrNotFound
	}

	// Remove the orphaned FTS entry (not cascade-deleted automatically).
	if _, err := s.writer.ExecContext(ctx, `
		DELETE FROM pipelines_fts WHERE rowid IN (
			SELECT rowid FROM pipelines_fts WHERE id = ?
		)
	`, id); err != nil {
		return fmt.Errorf("failed to delete pipelines_fts entry: %w", err)
	}

	// Merge FTS5 index segments to keep search fast.
	if _, err := s.writer.ExecContext(ctx, `INSERT INTO pipelines_fts(pipelines_fts) VALUES('optimize')`); err != nil {
		return fmt.Errorf("failed to optimize pipelines_fts: %w", err)
	}

	if _, err := s.writer.ExecContext(ctx, `INSERT INTO data_fts(data_fts) VALUES('optimize')`); err != nil {
		return fmt.Errorf("failed to optimize data_fts: %w", err)
	}

	// Update query-planner statistics.
	if _, err := s.writer.ExecContext(ctx, `PRAGMA optimize`); err != nil {
		return fmt.Errorf("failed to run PRAGMA optimize: %w", err)
	}

	// Reclaim disk space freed by the deleted rows and their cascades.
	if _, err := s.writer.ExecContext(ctx, `VACUUM`); err != nil {
		return fmt.Errorf("failed to vacuum after delete: %w", err)
	}

	return nil
}

// UpdatePipelineResumeEnabled updates the resume_enabled flag for a pipeline.
func (s *Sqlite) UpdatePipelineResumeEnabled(ctx context.Context, pipelineID string, enabled bool) error {
	val := 0
	if enabled {
		val = 1
	}

	result, err := s.writer.ExecContext(ctx, `UPDATE pipelines SET resume_enabled = ? WHERE id = ?`, val, pipelineID)
	if err != nil {
		return fmt.Errorf("failed to update pipeline resume_enabled: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return storage.ErrNotFound
	}

	return nil
}

// UpdatePipelineRBACExpression updates the RBAC expression for a pipeline.
func (s *Sqlite) UpdatePipelineRBACExpression(ctx context.Context, pipelineID, expression string) error {
	result, err := s.writer.ExecContext(ctx, `UPDATE pipelines SET rbac_expression = ? WHERE id = ?`, expression, pipelineID)
	if err != nil {
		return fmt.Errorf("failed to update pipeline rbac_expression: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return storage.ErrNotFound
	}

	return nil
}

// SaveRun creates a new pipeline run record.
func (s *Sqlite) SaveRun(ctx context.Context, pipelineID string) (*storage.PipelineRun, error) {
	id := support.UniqueID()
	now := time.Now().UTC()

	_, err := s.writer.ExecContext(ctx, `
		INSERT INTO pipeline_runs (id, pipeline_id, status, created_at)
		VALUES (?, ?, ?, ?)
	`, id, pipelineID, storage.RunStatusQueued, now.Unix())
	if err != nil {
		return nil, fmt.Errorf("failed to save run: %w", err)
	}

	return &storage.PipelineRun{
		ID:         id,
		PipelineID: pipelineID,
		Status:     storage.RunStatusQueued,
		CreatedAt:  now,
	}, nil
}

// GetRun retrieves a pipeline run by its ID.
func (s *Sqlite) GetRun(ctx context.Context, runID string) (*storage.PipelineRun, error) {
	var row pipelineRunScan

	err := sqlscan.Get(ctx, s.writer, &row, `
		SELECT id, pipeline_id, status, started_at, completed_at, error_message, created_at
		FROM pipeline_runs WHERE id = ?
	`, runID)
	if err != nil {
		if sqlscan.NotFound(err) {
			return nil, storage.ErrNotFound
		}

		return nil, fmt.Errorf("failed to get run: %w", err)
	}

	run := row.toStorage()

	return &run, nil
}

// GetRunsByStatus returns all pipeline runs with the given status.
func (s *Sqlite) GetRunsByStatus(ctx context.Context, status storage.RunStatus) ([]storage.PipelineRun, error) {
	var rows []pipelineRunScan

	err := sqlscan.Select(ctx, s.writer, &rows, `
		SELECT id, pipeline_id, status, started_at, completed_at, error_message, created_at
		FROM pipeline_runs WHERE status = ?
		ORDER BY created_at DESC
	`, string(status))
	if err != nil {
		return nil, fmt.Errorf("failed to get runs by status: %w", err)
	}

	runs := make([]storage.PipelineRun, 0, len(rows))
	for _, row := range rows {
		runs = append(runs, row.toStorage())
	}

	return runs, nil
}

// GetRunStats returns the count of pipeline runs grouped by status.
func (s *Sqlite) GetRunStats(ctx context.Context) (map[storage.RunStatus]int, error) {
	type row struct {
		Status string `db:"status"`
		Count  int    `db:"count"`
	}

	var rows []row

	err := sqlscan.Select(ctx, s.writer, &rows, `SELECT status, COUNT(*) AS count FROM pipeline_runs GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("failed to get run stats: %w", err)
	}

	stats := make(map[storage.RunStatus]int, len(rows))
	for _, r := range rows {
		stats[storage.RunStatus(r.Status)] = r.Count
	}

	return stats, nil
}

// GetRecentRunsByStatus returns the most recent N pipeline runs with the given status.
func (s *Sqlite) GetRecentRunsByStatus(ctx context.Context, status storage.RunStatus, limit int) ([]storage.PipelineRun, error) {
	var rows []pipelineRunScan

	err := sqlscan.Select(ctx, s.writer, &rows, `
		SELECT id, pipeline_id, status, started_at, completed_at, error_message, created_at
		FROM pipeline_runs WHERE status = ?
		ORDER BY created_at DESC
		LIMIT ?
	`, string(status), limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get recent runs by status: %w", err)
	}

	runs := make([]storage.PipelineRun, 0, len(rows))
	for _, row := range rows {
		runs = append(runs, row.toStorage())
	}

	return runs, nil
}

// SearchRunsByPipeline returns a paginated list of runs for a specific pipeline
// filtered by query matching the run ID, status, or error message using FTS5.
// When query is empty it returns all runs ordered by creation date descending.
func (s *Sqlite) SearchRunsByPipeline(ctx context.Context, pipelineID, query string, page, perPage int) (*storage.PaginationResult[storage.PipelineRun], error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 20
	}

	offset := (page - 1) * perPage

	if query == "" {
		// No FTS filter – return all runs ordered by creation date.
		var totalItems int
		err := sqlscan.Get(ctx, s.writer, &totalItems, `SELECT COUNT(*) FROM pipeline_runs WHERE pipeline_id = ?`, pipelineID)
		if err != nil {
			return nil, fmt.Errorf("failed to count runs: %w", err)
		}

		var rows []pipelineRunScan
		err = sqlscan.Select(ctx, s.writer, &rows, `
			SELECT id, pipeline_id, status, started_at, completed_at, error_message, created_at
			FROM pipeline_runs WHERE pipeline_id = ?
			ORDER BY created_at DESC
			LIMIT ? OFFSET ?
		`, pipelineID, perPage, offset)
		if err != nil {
			return nil, fmt.Errorf("failed to list runs: %w", err)
		}

		runs := make([]storage.PipelineRun, 0, len(rows))
		for _, row := range rows {
			runs = append(runs, row.toStorage())
		}

		totalPages := (totalItems + perPage - 1) / perPage

		return &storage.PaginationResult[storage.PipelineRun]{
			Items:      runs,
			Page:       page,
			PerPage:    perPage,
			TotalItems: totalItems,
			TotalPages: totalPages,
			HasNext:    page < totalPages,
		}, nil
	}

	ftsQuery := sanitizeFTSQuery(query)

	var totalItems int
	err := sqlscan.Get(ctx, s.writer, &totalItems, `
		SELECT COUNT(*) FROM pipeline_runs
		WHERE pipeline_id = ?
		  AND id IN (SELECT id FROM pipeline_runs_fts WHERE pipeline_runs_fts MATCH ?)
	`, pipelineID, ftsQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to count run search results: %w", err)
	}

	var rows []pipelineRunScan

	err = sqlscan.Select(ctx, s.writer, &rows, `
		SELECT id, pipeline_id, status, started_at, completed_at, error_message, created_at
		FROM pipeline_runs
		WHERE pipeline_id = ?
		  AND id IN (SELECT id FROM pipeline_runs_fts WHERE pipeline_runs_fts MATCH ?)
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?
	`, pipelineID, ftsQuery, perPage, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to search runs: %w", err)
	}

	runs := make([]storage.PipelineRun, 0, len(rows))
	for _, row := range rows {
		runs = append(runs, row.toStorage())
	}

	totalPages := (totalItems + perPage - 1) / perPage

	return &storage.PaginationResult[storage.PipelineRun]{
		Items:      runs,
		Page:       page,
		PerPage:    perPage,
		TotalItems: totalItems,
		TotalPages: totalPages,
		HasNext:    page < totalPages,
	}, nil
}

func (s *Sqlite) UpdateRunStatus(ctx context.Context, runID string, status storage.RunStatus, errorMessage string) error {
	now := time.Now().UTC()

	var query string
	var args []any

	switch status {
	case storage.RunStatusRunning:
		query = `UPDATE pipeline_runs SET status = ?, started_at = ? WHERE id = ?`
		args = []any{status, now.Unix(), runID}
	case storage.RunStatusSuccess, storage.RunStatusFailed, storage.RunStatusSkipped:
		query = `UPDATE pipeline_runs SET status = ?, completed_at = ?, error_message = ? WHERE id = ?`
		args = []any{status, now.Unix(), errorMessage, runID}
	default:
		query = `UPDATE pipeline_runs SET status = ? WHERE id = ?`
		args = []any{status, runID}
	}

	result, err := s.writer.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to update run status: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return storage.ErrNotFound
	}

	return nil
}

// SearchPipelines returns pipelines whose name or content contain query using
// the FTS5 index. When query is empty it returns all pipelines ordered by
// creation date descending.
func (s *Sqlite) SearchPipelines(ctx context.Context, query string, page, perPage int) (*storage.PaginationResult[storage.Pipeline], error) {
	if page < 1 {
		page = 1
	}

	if perPage < 1 {
		perPage = 20
	}

	offset := (page - 1) * perPage

	if query == "" {
		// No FTS filter – return all pipelines ordered by creation date.
		var totalItems int
		err := sqlscan.Get(ctx, s.writer, &totalItems, `SELECT COUNT(*) FROM pipelines`)
		if err != nil {
			return nil, fmt.Errorf("failed to count pipelines: %w", err)
		}

		var rows []pipelineScan
		err = sqlscan.Select(ctx, s.writer, &rows, `
			SELECT id, name, content, content_type, driver, resume_enabled, rbac_expression, created_at, updated_at
			FROM pipelines ORDER BY created_at DESC
			LIMIT ? OFFSET ?
		`, perPage, offset)
		if err != nil {
			return nil, fmt.Errorf("failed to list pipelines: %w", err)
		}

		pipelines := make([]storage.Pipeline, 0, len(rows))
		for _, row := range rows {
			pipelines = append(pipelines, row.toStorage())
		}

		totalPages := (totalItems + perPage - 1) / perPage

		return &storage.PaginationResult[storage.Pipeline]{
			Items:      pipelines,
			Page:       page,
			PerPage:    perPage,
			TotalItems: totalItems,
			TotalPages: totalPages,
			HasNext:    page < totalPages,
		}, nil
	}

	ftsQuery := sanitizeFTSQuery(query)

	var totalItems int

	err := sqlscan.Get(ctx, s.writer, &totalItems, `
		SELECT COUNT(*) FROM pipelines
		WHERE id IN (SELECT id FROM pipelines_fts WHERE pipelines_fts MATCH ?)
	`, ftsQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to count pipeline search results: %w", err)
	}

	var rows []pipelineScan

	err = sqlscan.Select(ctx, s.writer, &rows, `
		SELECT p.id, p.name, p.content, p.content_type, p.driver, p.resume_enabled, p.rbac_expression, p.created_at, p.updated_at
		FROM pipelines p
		WHERE p.id IN (SELECT id FROM pipelines_fts WHERE pipelines_fts MATCH ?)
		ORDER BY p.created_at DESC
		LIMIT ? OFFSET ?
	`, ftsQuery, perPage, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to search pipelines: %w", err)
	}

	pipelines := make([]storage.Pipeline, 0, len(rows))
	for _, row := range rows {
		pipelines = append(pipelines, row.toStorage())
	}

	totalPages := (totalItems + perPage - 1) / perPage

	return &storage.PaginationResult[storage.Pipeline]{
		Items:      pipelines,
		Page:       page,
		PerPage:    perPage,
		TotalItems: totalItems,
		TotalPages: totalPages,
		HasNext:    page < totalPages,
	}, nil
}

// Search returns records whose indexed text matches query and whose path begins
// with prefix. prefix follows the same convention as Set (no namespace prefix).
func (s *Sqlite) Search(ctx context.Context, prefix, query string) (storage.Results, error) {
	if query == "" {
		return nil, nil
	}

	ftsQuery := sanitizeFTSQuery(query)
	fullPrefix := filepath.Clean("/" + s.namespace + "/" + prefix)

	var results storage.Results

	err := sqlscan.Select(ctx, s.reader, &results, `
		SELECT
			COALESCE(t.id, 0) AS id,
			f.path AS path,
			COALESCE(
				json_object(
					'status',     t.status,
					'elapsed',    t.elapsed,
					'started_at', t.started_at
				),
				'{}'
			) AS payload
		FROM data_fts f
		LEFT JOIN tasks t ON t.path = f.path
		WHERE data_fts MATCH ? AND f.path LIKE ? || '/%'
		ORDER BY COALESCE(t.id, f.rowid) ASC
	`, ftsQuery, fullPrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to search: %w", err)
	}

	return results, nil
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
