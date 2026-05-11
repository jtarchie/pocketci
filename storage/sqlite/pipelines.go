package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/georgysavva/scany/v2/sqlscan"
	"github.com/jtarchie/pocketci/runtime/support"
	"github.com/jtarchie/pocketci/storage"
)

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
	_, err = s.writer.ExecContext(ctx, `
		DELETE FROM pipelines_fts WHERE rowid IN (
			SELECT rowid FROM pipelines_fts WHERE id = ?
		)
	`, id)
	if err != nil {
		return fmt.Errorf("failed to delete pipelines_fts entry: %w", err)
	}

	// Merge FTS5 index segments to keep search fast.
	_, err = s.writer.ExecContext(ctx, `INSERT INTO pipelines_fts(pipelines_fts) VALUES('optimize')`)
	if err != nil {
		return fmt.Errorf("failed to optimize pipelines_fts: %w", err)
	}

	_, err = s.writer.ExecContext(ctx, `INSERT INTO data_fts(data_fts) VALUES('optimize')`)
	if err != nil {
		return fmt.Errorf("failed to optimize data_fts: %w", err)
	}

	// Update query-planner statistics.
	_, err = s.writer.ExecContext(ctx, `PRAGMA optimize`)
	if err != nil {
		return fmt.Errorf("failed to run PRAGMA optimize: %w", err)
	}

	// Reclaim disk space freed by the deleted rows and their cascades.
	_, err = s.writer.ExecContext(ctx, `VACUUM`)
	if err != nil {
		return fmt.Errorf("failed to vacuum after delete: %w", err)
	}

	return nil
}

// UpdatePipeline applies partial updates to a pipeline. Only non-nil fields
// in the PipelineUpdate struct are written.
func (s *Sqlite) UpdatePipeline(ctx context.Context, pipelineID string, update storage.PipelineUpdate) error {
	var setClauses []string

	var args []any

	if update.ResumeEnabled != nil {
		setClauses = append(setClauses, "resume_enabled = ?")
		args = append(args, boolToInt(*update.ResumeEnabled))
	}

	if update.Paused != nil {
		setClauses = append(setClauses, "paused = ?")
		args = append(args, boolToInt(*update.Paused))
	}

	if update.RBACExpression != nil {
		setClauses = append(setClauses, "rbac_expression = ?")
		args = append(args, *update.RBACExpression)
	}

	if len(setClauses) == 0 {
		return nil
	}

	args = append(args, pipelineID)

	query := "UPDATE pipelines SET " + strings.Join(setClauses, ", ") + " WHERE id = ?"

	result, err := s.writer.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to update pipeline: %w", err)
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
	const cols = `id, name, content, content_type, driver, resume_enabled, paused, rbac_expression, created_at, updated_at`

	return paginatedSearch[pipelineScan](
		ctx, s.writer, page, perPage, query,
		`SELECT COUNT(*) FROM pipelines`,
		`SELECT `+cols+` FROM pipelines
			ORDER BY created_at DESC`,
		`SELECT COUNT(*) FROM pipelines
			WHERE id IN (SELECT id FROM pipelines_fts WHERE pipelines_fts MATCH ?)`,
		`SELECT `+cols+` FROM pipelines
			WHERE id IN (SELECT id FROM pipelines_fts WHERE pipelines_fts MATCH ?)
			ORDER BY created_at DESC`,
		nil,
		pipelineScan.toStorage,
	)
}
