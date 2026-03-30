package storage

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
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
	Paused         int    `db:"paused"`
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
		Paused:         p.Paused != 0,
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
	TriggerType  string         `db:"trigger_type"`
	TriggeredBy  string         `db:"triggered_by"`
	TriggerInput string         `db:"trigger_input"`
	CreatedAt    int64          `db:"created_at"`
}

func (p pipelineRunScan) toStorage() storage.PipelineRun {
	run := storage.PipelineRun{
		ID:          p.ID,
		PipelineID:  p.PipelineID,
		Status:      storage.RunStatus(p.Status),
		TriggerType: storage.TriggerType(p.TriggerType),
		TriggeredBy: p.TriggeredBy,
		CreatedAt:   time.Unix(p.CreatedAt, 0).UTC(),
	}

	if p.TriggerInput != "" {
		_ = json.Unmarshal([]byte(p.TriggerInput), &run.TriggerInput)
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

// escapeLike escapes SQL LIKE wildcard characters (%, _, \) in a string
// so it can be used safely in a LIKE pattern with ESCAPE '\'.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)

	return s
}

func (s *Sqlite) GetMostRecentJobStatus(ctx context.Context, pipelineID, jobName string) (string, error) {
	escaped := escapeLike(jobName)

	var status string

	var err error

	if s.namespace != "" {
		// CLI mode: namespace already scopes to pipeline
		pattern := "/" + s.namespace + "/pipeline/%/jobs/" + escaped
		err = s.reader.QueryRowContext(ctx, `
			SELECT status FROM tasks
			WHERE path LIKE ? ESCAPE '\'
			  AND status NOT IN ('skipped', 'pending')
			ORDER BY id DESC LIMIT 1
		`, pattern).Scan(&status)
	} else {
		// Server mode: join with pipeline_runs for pipeline scoping
		err = s.reader.QueryRowContext(ctx, `
			SELECT t.status FROM tasks t
			JOIN pipeline_runs pr ON t.run_id = pr.id
			WHERE pr.pipeline_id = ?
			  AND t.path LIKE '%/jobs/' || ? ESCAPE '\'
			  AND t.status NOT IN ('skipped', 'pending')
			ORDER BY t.id DESC LIMIT 1
		`, pipelineID, escaped).Scan(&status)
	}

	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("failed to get most recent job status: %w", err)
	}

	return status, nil
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
		SELECT id, name, content, content_type, driver, resume_enabled, paused, rbac_expression, created_at, updated_at
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
		SELECT id, name, content, content_type, driver, resume_enabled, paused, rbac_expression, created_at, updated_at
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

// UpdatePipelinePaused updates the paused flag for a pipeline.
func (s *Sqlite) UpdatePipelinePaused(ctx context.Context, pipelineID string, paused bool) error {
	val := 0
	if paused {
		val = 1
	}

	result, err := s.writer.ExecContext(ctx, `UPDATE pipelines SET paused = ? WHERE id = ?`, val, pipelineID)
	if err != nil {
		return fmt.Errorf("failed to update pipeline paused: %w", err)
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
func (s *Sqlite) SaveRun(ctx context.Context, pipelineID string, triggerType storage.TriggerType, triggeredBy string, triggerInput storage.TriggerInput) (*storage.PipelineRun, error) {
	id := support.UniqueID()
	now := time.Now().UTC()

	inputJSON, err := json.Marshal(triggerInput)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal trigger input: %w", err)
	}

	_, err = s.writer.ExecContext(ctx, `
		INSERT INTO pipeline_runs (id, pipeline_id, status, trigger_type, triggered_by, trigger_input, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, id, pipelineID, storage.RunStatusQueued, string(triggerType), triggeredBy, string(inputJSON), now.Unix())
	if err != nil {
		return nil, fmt.Errorf("failed to save run: %w", err)
	}

	return &storage.PipelineRun{
		ID:           id,
		PipelineID:   pipelineID,
		Status:       storage.RunStatusQueued,
		TriggerType:  triggerType,
		TriggeredBy:  triggeredBy,
		TriggerInput: triggerInput,
		CreatedAt:    now,
	}, nil
}

// GetRun retrieves a pipeline run by its ID.
func (s *Sqlite) GetRun(ctx context.Context, runID string) (*storage.PipelineRun, error) {
	var row pipelineRunScan

	err := sqlscan.Get(ctx, s.writer, &row, `
		SELECT id, pipeline_id, status, started_at, completed_at, error_message, trigger_type, triggered_by, trigger_input, created_at
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
		SELECT id, pipeline_id, status, started_at, completed_at, error_message, trigger_type, triggered_by, trigger_input, created_at
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
		SELECT id, pipeline_id, status, started_at, completed_at, error_message, trigger_type, triggered_by, trigger_input, created_at
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
			SELECT id, pipeline_id, status, started_at, completed_at, error_message, trigger_type, triggered_by, trigger_input, created_at
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
		SELECT id, pipeline_id, status, started_at, completed_at, error_message, trigger_type, triggered_by, trigger_input, created_at
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
	now := time.Now().UTC().Unix()

	result, err := s.writer.ExecContext(ctx, `
		UPDATE pipeline_runs
		SET
			status        = ?1,
			started_at    = CASE WHEN ?1 = 'running' THEN ?2 ELSE started_at END,
			completed_at  = CASE WHEN ?1 IN ('success', 'failed', 'skipped') THEN ?2 ELSE completed_at END,
			error_message = CASE WHEN ?1 IN ('success', 'failed', 'skipped') THEN ?3 ELSE error_message END
		WHERE id = ?4
		  AND (
			  ?1 IN ('failed', 'queued')
			  OR status IN ('queued', 'running')
		  )
	`, string(status), now, errorMessage, runID)
	if err != nil {
		return fmt.Errorf("failed to update run status: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		// Distinguish "run doesn't exist" from "transition blocked by state machine".
		var exists int
		_ = s.writer.QueryRowContext(ctx, `SELECT 1 FROM pipeline_runs WHERE id = ?`, runID).Scan(&exists)

		if exists == 1 {
			return nil // transition blocked — silent no-op
		}

		return storage.ErrNotFound
	}

	return nil
}

// PruneRunsByPipeline deletes old pipeline runs for a pipeline.
// If keepBuilds > 0, runs beyond the N most recent are deleted.
// If cutoffTime is non-nil, runs created before that time are deleted.
// Both constraints are applied independently.
// The cascade trigger pipeline_runs_tasks_delete handles associated task cleanup.
func (s *Sqlite) PruneRunsByPipeline(ctx context.Context, pipelineID string, keepBuilds int, cutoffTime *time.Time) error {
	if keepBuilds > 0 {
		_, err := s.writer.ExecContext(ctx, `
			DELETE FROM pipeline_runs
			WHERE pipeline_id = ?
			  AND id NOT IN (
			    SELECT id FROM pipeline_runs
			    WHERE pipeline_id = ?
			    ORDER BY created_at DESC
			    LIMIT ?
			  )
		`, pipelineID, pipelineID, keepBuilds)
		if err != nil {
			return fmt.Errorf("failed to prune runs by count: %w", err)
		}
	}

	if cutoffTime != nil {
		_, err := s.writer.ExecContext(ctx, `
			DELETE FROM pipeline_runs
			WHERE pipeline_id = ?
			  AND created_at < ?
		`, pipelineID, cutoffTime.Unix())
		if err != nil {
			return fmt.Errorf("failed to prune runs by age: %w", err)
		}
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
			SELECT id, name, content, content_type, driver, resume_enabled, paused, rbac_expression, created_at, updated_at
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
		SELECT p.id, p.name, p.content, p.content_type, p.driver, p.resume_enabled, p.paused, p.rbac_expression, p.created_at, p.updated_at
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

// CheckWebhookDedup returns true if keyHash has already been recorded for pipelineID.
func (s *Sqlite) CheckWebhookDedup(ctx context.Context, pipelineID string, keyHash []byte) (bool, error) {
	var exists int

	err := s.reader.QueryRowContext(ctx,
		`SELECT 1 FROM webhook_dedup WHERE pipeline_id = ? AND key_hash = ? LIMIT 1`,
		pipelineID, keyHash,
	).Scan(&exists)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}

		return false, fmt.Errorf("check webhook dedup: %w", err)
	}

	return true, nil
}

// SaveWebhookDedup records keyHash for pipelineID. Duplicate inserts are silently ignored.
func (s *Sqlite) SaveWebhookDedup(ctx context.Context, pipelineID string, keyHash []byte) error {
	_, err := s.writer.ExecContext(ctx,
		`INSERT OR IGNORE INTO webhook_dedup (pipeline_id, key_hash) VALUES (?, ?)`,
		pipelineID, keyHash,
	)
	if err != nil {
		return fmt.Errorf("save webhook dedup: %w", err)
	}

	return nil
}

// PruneWebhookDedup deletes dedup entries created before olderThan and returns the count removed.
func (s *Sqlite) PruneWebhookDedup(ctx context.Context, olderThan time.Time) (int64, error) {
	result, err := s.writer.ExecContext(ctx,
		`DELETE FROM webhook_dedup WHERE created_at < ?`,
		olderThan.Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("prune webhook dedup: %w", err)
	}

	return result.RowsAffected()
}

// scheduleScan is an intermediate struct for scanning schedule rows.
type scheduleScan struct {
	ID           string        `db:"id"`
	PipelineID   string        `db:"pipeline_id"`
	Name         string        `db:"name"`
	ScheduleType string        `db:"schedule_type"`
	ScheduleExpr string        `db:"schedule_expr"`
	JobName      string        `db:"job_name"`
	Enabled      int           `db:"enabled"`
	LastRunAt    sql.NullInt64 `db:"last_run_at"`
	NextRunAt    sql.NullInt64 `db:"next_run_at"`
	CreatedAt    int64         `db:"created_at"`
	UpdatedAt    int64         `db:"updated_at"`
}

func (s scheduleScan) toStorage() storage.Schedule {
	sched := storage.Schedule{
		ID:           s.ID,
		PipelineID:   s.PipelineID,
		Name:         s.Name,
		ScheduleType: storage.ScheduleType(s.ScheduleType),
		ScheduleExpr: s.ScheduleExpr,
		JobName:      s.JobName,
		Enabled:      s.Enabled != 0,
		CreatedAt:    time.Unix(s.CreatedAt, 0).UTC(),
		UpdatedAt:    time.Unix(s.UpdatedAt, 0).UTC(),
	}

	if s.LastRunAt.Valid {
		t := time.Unix(s.LastRunAt.Int64, 0).UTC()
		sched.LastRunAt = &t
	}

	if s.NextRunAt.Valid {
		t := time.Unix(s.NextRunAt.Int64, 0).UTC()
		sched.NextRunAt = &t
	}

	return sched
}

// SaveSchedule creates or updates a schedule in the database.
// On conflict (same pipeline_id + name), it updates the expression and job name
// but preserves user-managed fields (enabled, last_run_at).
func (s *Sqlite) SaveSchedule(ctx context.Context, schedule *storage.Schedule) error {
	now := time.Now().UTC()

	var nextRunAt *int64
	if schedule.NextRunAt != nil {
		v := schedule.NextRunAt.Unix()
		nextRunAt = &v
	}

	_, err := s.writer.ExecContext(ctx, `
		INSERT INTO schedules (id, pipeline_id, name, schedule_type, schedule_expr, job_name, enabled, next_run_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(pipeline_id, name) DO UPDATE SET
			schedule_type = excluded.schedule_type,
			schedule_expr = excluded.schedule_expr,
			job_name      = excluded.job_name,
			next_run_at   = excluded.next_run_at,
			updated_at    = excluded.updated_at
	`, schedule.ID, schedule.PipelineID, schedule.Name, string(schedule.ScheduleType),
		schedule.ScheduleExpr, schedule.JobName, boolToInt(schedule.Enabled), nextRunAt, now.Unix(), now.Unix())
	if err != nil {
		return fmt.Errorf("failed to save schedule: %w", err)
	}

	return nil
}

// GetSchedulesByPipeline returns all schedules for a pipeline.
func (s *Sqlite) GetSchedulesByPipeline(ctx context.Context, pipelineID string) ([]storage.Schedule, error) {
	var rows []scheduleScan

	err := sqlscan.Select(ctx, s.reader, &rows, `
		SELECT id, pipeline_id, name, schedule_type, schedule_expr, job_name, enabled, last_run_at, next_run_at, created_at, updated_at
		FROM schedules WHERE pipeline_id = ?
		ORDER BY name ASC
	`, pipelineID)
	if err != nil {
		return nil, fmt.Errorf("failed to get schedules by pipeline: %w", err)
	}

	schedules := make([]storage.Schedule, 0, len(rows))
	for _, row := range rows {
		schedules = append(schedules, row.toStorage())
	}

	return schedules, nil
}

// DeleteSchedulesByPipeline removes all schedules for a pipeline.
func (s *Sqlite) DeleteSchedulesByPipeline(ctx context.Context, pipelineID string) error {
	_, err := s.writer.ExecContext(ctx, `DELETE FROM schedules WHERE pipeline_id = ?`, pipelineID)
	if err != nil {
		return fmt.Errorf("failed to delete schedules by pipeline: %w", err)
	}

	return nil
}

// DeleteSchedulesByPipelineExcept removes schedules for a pipeline whose names are NOT in keepNames.
func (s *Sqlite) DeleteSchedulesByPipelineExcept(ctx context.Context, pipelineID string, keepNames []string) error {
	if len(keepNames) == 0 {
		return s.DeleteSchedulesByPipeline(ctx, pipelineID)
	}

	placeholders := make([]string, len(keepNames))
	args := make([]any, 0, len(keepNames)+1)
	args = append(args, pipelineID)

	for i, name := range keepNames {
		placeholders[i] = "?"
		args = append(args, name)
	}

	query := fmt.Sprintf(
		`DELETE FROM schedules WHERE pipeline_id = ? AND name NOT IN (%s)`,
		strings.Join(placeholders, ", "),
	)

	_, err := s.writer.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to delete stale schedules: %w", err)
	}

	return nil
}

// UpdateScheduleEnabled updates the enabled flag for a schedule.
func (s *Sqlite) UpdateScheduleEnabled(ctx context.Context, id string, enabled bool) error {
	result, err := s.writer.ExecContext(ctx,
		`UPDATE schedules SET enabled = ?, updated_at = ? WHERE id = ?`,
		boolToInt(enabled), time.Now().UTC().Unix(), id,
	)
	if err != nil {
		return fmt.Errorf("failed to update schedule enabled: %w", err)
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

// ClaimDueSchedules atomically claims schedules whose next_run_at <= now.
// Uses UPDATE...RETURNING to prevent multi-instance collisions.
func (s *Sqlite) ClaimDueSchedules(ctx context.Context, now time.Time) ([]storage.Schedule, error) {
	rows, err := s.writer.QueryContext(ctx, `
		UPDATE schedules
		SET last_run_at = ?, next_run_at = NULL
		WHERE id IN (
			SELECT id FROM schedules
			WHERE enabled = 1 AND next_run_at IS NOT NULL AND next_run_at <= ?
		)
		RETURNING id, pipeline_id, name, schedule_type, schedule_expr, job_name, enabled, last_run_at, next_run_at, created_at, updated_at
	`, now.Unix(), now.Unix())
	if err != nil {
		return nil, fmt.Errorf("failed to claim due schedules: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var schedules []storage.Schedule

	for rows.Next() {
		var row scheduleScan

		err := rows.Scan(
			&row.ID, &row.PipelineID, &row.Name, &row.ScheduleType, &row.ScheduleExpr,
			&row.JobName, &row.Enabled, &row.LastRunAt, &row.NextRunAt, &row.CreatedAt, &row.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan claimed schedule: %w", err)
		}

		schedules = append(schedules, row.toStorage())
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate claimed schedules: %w", err)
	}

	return schedules, nil
}

// UpdateScheduleAfterRun updates the last_run_at and next_run_at for a schedule after it fires.
func (s *Sqlite) UpdateScheduleAfterRun(ctx context.Context, id string, lastRunAt, nextRunAt time.Time) error {
	_, err := s.writer.ExecContext(ctx, `
		UPDATE schedules SET last_run_at = ?, next_run_at = ?, updated_at = ? WHERE id = ?
	`, lastRunAt.Unix(), nextRunAt.Unix(), time.Now().UTC().Unix(), id)
	if err != nil {
		return fmt.Errorf("failed to update schedule after run: %w", err)
	}

	return nil
}

const gateColumns = `id, run_id, pipeline_id, name, status, message, approved_by, created_at, resolved_at`

// gateScan is an intermediate struct for scanning gate rows.
type gateScan struct {
	ID         string        `db:"id"`
	RunID      string        `db:"run_id"`
	PipelineID string        `db:"pipeline_id"`
	Name       string        `db:"name"`
	Status     string        `db:"status"`
	Message    string        `db:"message"`
	ApprovedBy string        `db:"approved_by"`
	CreatedAt  int64         `db:"created_at"`
	ResolvedAt sql.NullInt64 `db:"resolved_at"`
}

func (g gateScan) toStorage() storage.Gate {
	gate := storage.Gate{
		ID:         g.ID,
		RunID:      g.RunID,
		PipelineID: g.PipelineID,
		Name:       g.Name,
		Status:     storage.GateStatus(g.Status),
		Message:    g.Message,
		ApprovedBy: g.ApprovedBy,
		CreatedAt:  time.Unix(g.CreatedAt, 0).UTC(),
	}

	if g.ResolvedAt.Valid {
		t := time.Unix(g.ResolvedAt.Int64, 0).UTC()
		gate.ResolvedAt = &t
	}

	return gate
}

// SaveGate creates a new gate record.
func (s *Sqlite) SaveGate(ctx context.Context, gate *storage.Gate) error {
	_, err := s.writer.ExecContext(ctx, `
		INSERT INTO gates (id, run_id, pipeline_id, name, status, message, approved_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, gate.ID, gate.RunID, gate.PipelineID, gate.Name, string(gate.Status),
		gate.Message, gate.ApprovedBy, time.Now().UTC().Unix())
	if err != nil {
		return fmt.Errorf("failed to save gate: %w", err)
	}

	return nil
}

// GetGate returns a single gate by ID.
func (s *Sqlite) GetGate(ctx context.Context, gateID string) (*storage.Gate, error) {
	var row gateScan

	err := sqlscan.Get(ctx, s.reader, &row,
		`SELECT `+gateColumns+` FROM gates WHERE id = ?`,
		gateID)
	if err != nil {
		return nil, fmt.Errorf("failed to get gate: %w", err)
	}

	gate := row.toStorage()

	return &gate, nil
}

// GetPendingGates returns all gates with status 'pending'.
func (s *Sqlite) GetPendingGates(ctx context.Context) ([]storage.Gate, error) {
	var rows []gateScan

	err := sqlscan.Select(ctx, s.reader, &rows,
		`SELECT `+gateColumns+` FROM gates WHERE status = 'pending' ORDER BY created_at ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get pending gates: %w", err)
	}

	gates := make([]storage.Gate, 0, len(rows))
	for _, row := range rows {
		gates = append(gates, row.toStorage())
	}

	return gates, nil
}

// ResolveGate updates a gate's status and records who resolved it.
func (s *Sqlite) ResolveGate(ctx context.Context, gateID string, status storage.GateStatus, approvedBy string) error {
	now := time.Now().UTC().Unix()

	result, err := s.writer.ExecContext(ctx, `
		UPDATE gates SET status = ?, approved_by = ?, resolved_at = ?
		WHERE id = ? AND status = 'pending'
	`, string(status), approvedBy, now, gateID)
	if err != nil {
		return fmt.Errorf("failed to resolve gate: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}

	if rows == 0 {
		return fmt.Errorf("gate %s: %w", gateID, storage.ErrNotFound)
	}

	return nil
}

// GetGatesByRunID returns all gates for a specific pipeline run.
func (s *Sqlite) GetGatesByRunID(ctx context.Context, runID string) ([]storage.Gate, error) {
	var rows []gateScan

	err := sqlscan.Select(ctx, s.reader, &rows,
		`SELECT `+gateColumns+` FROM gates WHERE run_id = ? ORDER BY created_at ASC`,
		runID)
	if err != nil {
		return nil, fmt.Errorf("failed to get gates by run ID: %w", err)
	}

	gates := make([]storage.Gate, 0, len(rows))
	for _, row := range rows {
		gates = append(gates, row.toStorage())
	}

	return gates, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}

	return 0
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
