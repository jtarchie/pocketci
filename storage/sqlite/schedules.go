package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/georgysavva/scany/v2/sqlscan"
	"github.com/jtarchie/pocketci/storage"
)

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

// deleteAllSchedules removes all schedules for a pipeline.
func (s *Sqlite) deleteAllSchedules(ctx context.Context, pipelineID string) error {
	_, err := s.writer.ExecContext(ctx, `DELETE FROM schedules WHERE pipeline_id = ?`, pipelineID)
	if err != nil {
		return fmt.Errorf("failed to delete schedules by pipeline: %w", err)
	}

	return nil
}

// PruneSchedulesByPipeline removes schedules for a pipeline whose names are
// NOT in keepNames. An empty keepNames deletes all schedules for the pipeline.
func (s *Sqlite) PruneSchedulesByPipeline(ctx context.Context, pipelineID string, keepNames []string) error {
	if len(keepNames) == 0 {
		return s.deleteAllSchedules(ctx, pipelineID)
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

	err = rows.Err()
	if err != nil {
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
