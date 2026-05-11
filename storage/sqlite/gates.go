package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/georgysavva/scany/v2/sqlscan"
	"github.com/jtarchie/pocketci/storage"
)

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

func convertGateRows(rows []gateScan) []storage.Gate {
	gates := make([]storage.Gate, 0, len(rows))
	for _, row := range rows {
		gates = append(gates, row.toStorage())
	}

	return gates
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

	return convertGateRows(rows), nil
}
