package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/georgysavva/scany/v2/sqlscan"
	"github.com/jtarchie/pocketci/runtime/support"
	"github.com/jtarchie/pocketci/storage"
)

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

// GetRunsByStatus returns pipeline runs with the given status ordered by
// creation date descending. When limit > 0 at most limit rows are returned;
// limit <= 0 returns all matching rows.
func (s *Sqlite) GetRunsByStatus(ctx context.Context, status storage.RunStatus, limit int) ([]storage.PipelineRun, error) {
	query := `
		SELECT id, pipeline_id, status, started_at, completed_at, error_message, trigger_type, triggered_by, trigger_input, created_at
		FROM pipeline_runs WHERE status = ?
		ORDER BY created_at DESC`

	args := []any{string(status)}

	if limit > 0 {
		query += "\n\t\tLIMIT ?"
		args = append(args, limit)
	}

	var rows []pipelineRunScan

	err := sqlscan.Select(ctx, s.writer, &rows, query, args...)
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

// SearchRunsByPipeline returns a paginated list of runs for a specific pipeline
// filtered by query matching the run ID, status, or error message using FTS5.
// When query is empty it returns all runs ordered by creation date descending.
func (s *Sqlite) SearchRunsByPipeline(ctx context.Context, pipelineID, query string, page, perPage int) (*storage.PaginationResult[storage.PipelineRun], error) {
	const cols = `id, pipeline_id, status, started_at, completed_at, error_message, trigger_type, triggered_by, trigger_input, created_at`

	return paginatedSearch[pipelineRunScan](
		ctx, s.writer, page, perPage, query,
		`SELECT COUNT(*) FROM pipeline_runs WHERE pipeline_id = ?`,
		`SELECT `+cols+` FROM pipeline_runs WHERE pipeline_id = ?
			ORDER BY created_at DESC`,
		`SELECT COUNT(*) FROM pipeline_runs
			WHERE pipeline_id = ?
			  AND id IN (SELECT id FROM pipeline_runs_fts WHERE pipeline_runs_fts MATCH ?)`,
		`SELECT `+cols+` FROM pipeline_runs
			WHERE pipeline_id = ?
			  AND id IN (SELECT id FROM pipeline_runs_fts WHERE pipeline_runs_fts MATCH ?)
			ORDER BY created_at DESC`,
		[]any{pipelineID},
		pipelineRunScan.toStorage,
	)
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
