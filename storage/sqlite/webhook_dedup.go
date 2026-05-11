package storage

import (
	"context"
	"fmt"
	"time"
)

// RecordWebhookDedup atomically checks and records a dedup key.
// Returns true if the key already existed (duplicate), false if newly recorded.
func (s *Sqlite) RecordWebhookDedup(ctx context.Context, pipelineID string, keyHash []byte) (bool, error) {
	result, err := s.writer.ExecContext(ctx,
		`INSERT OR IGNORE INTO webhook_dedup (pipeline_id, key_hash) VALUES (?, ?)`,
		pipelineID, keyHash,
	)
	if err != nil {
		return false, fmt.Errorf("record webhook dedup: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("record webhook dedup rows affected: %w", err)
	}

	return rows == 0, nil
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

	n, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}

	return n, nil
}
