package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/georgysavva/scany/v2/sqlscan"

	"github.com/jtarchie/pocketci/storage"
)

type agentMemoryScan struct {
	ID           int64         `db:"id"`
	PipelineID   string        `db:"pipeline_id"`
	AgentName    string        `db:"agent_name"`
	ContentHash  string        `db:"content_hash"`
	Tags         string        `db:"tags"`
	Content      string        `db:"content"`
	RecallCount  int64         `db:"recall_count"`
	CreatedAt    int64         `db:"created_at"`
	LastRecalled sql.NullInt64 `db:"last_recalled"`
}

func (r agentMemoryScan) toStorage() storage.AgentMemory {
	mem := storage.AgentMemory{
		ID:          r.ID,
		PipelineID:  r.PipelineID,
		AgentName:   r.AgentName,
		ContentHash: r.ContentHash,
		Content:     r.Content,
		RecallCount: r.RecallCount,
		CreatedAt:   time.Unix(r.CreatedAt, 0).UTC(),
	}

	tags := []string{}
	if r.Tags != "" {
		_ = json.Unmarshal([]byte(r.Tags), &tags)
	}
	mem.Tags = tags

	if r.LastRecalled.Valid {
		t := time.Unix(r.LastRecalled.Int64, 0).UTC()
		mem.LastRecalled = &t
	}

	return mem
}

func hashMemoryContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// quoteFTSPhrase wraps a term in double quotes and escapes embedded quotes so
// FTS5's MATCH operator treats user input as a literal phrase instead of a
// query expression. Prevents syntax errors on punctuation like ":" or "*".
func quoteFTSPhrase(term string) string {
	return `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
}

func (s *Sqlite) SaveAgentMemory(
	ctx context.Context,
	pipelineID, agentName, content string,
	tags []string,
) (*storage.AgentMemory, bool, error) {
	if pipelineID == "" {
		return nil, false, errors.New("pipelineID is required")
	}

	if agentName == "" {
		return nil, false, errors.New("agentName is required")
	}

	if content == "" {
		return nil, false, errors.New("content is required")
	}

	if tags == nil {
		tags = []string{}
	}

	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return nil, false, fmt.Errorf("marshal tags: %w", err)
	}

	hash := hashMemoryContent(content)

	result, err := s.writer.ExecContext(ctx,
		`INSERT OR IGNORE INTO agent_memories (pipeline_id, agent_name, content_hash, tags, content)
		 VALUES (?, ?, ?, ?, ?)`,
		pipelineID, agentName, hash, string(tagsJSON), content,
	)
	if err != nil {
		return nil, false, fmt.Errorf("save agent memory: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return nil, false, fmt.Errorf("save agent memory rows affected: %w", err)
	}

	deduped := rows == 0

	var row agentMemoryScan

	err = sqlscan.Get(ctx, s.reader, &row,
		`SELECT id, pipeline_id, agent_name, content_hash, tags, content, recall_count, created_at, last_recalled
		 FROM agent_memories
		 WHERE pipeline_id = ? AND agent_name = ? AND content_hash = ?`,
		pipelineID, agentName, hash,
	)
	if err != nil {
		return nil, false, fmt.Errorf("fetch saved memory: %w", err)
	}

	mem := row.toStorage()
	return &mem, deduped, nil
}

func (s *Sqlite) RecallAgentMemories(
	ctx context.Context,
	pipelineID, agentName, query string,
	tags []string,
	limit int,
) ([]storage.AgentMemory, error) {
	if pipelineID == "" {
		return nil, errors.New("pipelineID is required")
	}

	if agentName == "" {
		return nil, errors.New("agentName is required")
	}

	if limit <= 0 {
		limit = 5
	}

	// Build an FTS5 MATCH expression from query + tags. An empty query falls
	// back to recency-ordered results (no FTS filter).
	var terms []string
	if strings.TrimSpace(query) != "" {
		terms = append(terms, quoteFTSPhrase(query))
	}
	for _, tag := range tags {
		if strings.TrimSpace(tag) == "" {
			continue
		}
		terms = append(terms, "tags:"+quoteFTSPhrase(tag))
	}

	var rows []agentMemoryScan

	if len(terms) == 0 {
		err := sqlscan.Select(ctx, s.reader, &rows,
			`SELECT id, pipeline_id, agent_name, content_hash, tags, content, recall_count, created_at, last_recalled
			 FROM agent_memories
			 WHERE pipeline_id = ? AND agent_name = ?
			 ORDER BY created_at DESC, id DESC
			 LIMIT ?`,
			pipelineID, agentName, limit,
		)
		if err != nil {
			return nil, fmt.Errorf("recall agent memories: %w", err)
		}
	} else {
		match := strings.Join(terms, " ")
		err := sqlscan.Select(ctx, s.reader, &rows,
			`SELECT m.id, m.pipeline_id, m.agent_name, m.content_hash, m.tags, m.content, m.recall_count, m.created_at, m.last_recalled
			 FROM agent_memories m
			 JOIN agent_memories_fts fts ON fts.rowid = m.id
			 WHERE m.pipeline_id = ? AND m.agent_name = ?
			   AND agent_memories_fts MATCH ?
			 ORDER BY fts.rank
			 LIMIT ?`,
			pipelineID, agentName, match, limit,
		)
		if err != nil {
			return nil, fmt.Errorf("recall agent memories: %w", err)
		}
	}

	if len(rows) == 0 {
		return []storage.AgentMemory{}, nil
	}

	ids := make([]any, 0, len(rows))
	placeholders := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
		placeholders = append(placeholders, "?")
	}

	now := time.Now().Unix()
	args := append([]any{now}, ids...)

	_, err := s.writer.ExecContext(ctx,
		fmt.Sprintf(`UPDATE agent_memories
		             SET recall_count = recall_count + 1, last_recalled = ?
		             WHERE id IN (%s)`, strings.Join(placeholders, ",")),
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("bump recall_count: %w", err)
	}

	out := make([]storage.AgentMemory, 0, len(rows))
	for _, r := range rows {
		mem := r.toStorage()
		mem.RecallCount++
		t := time.Unix(now, 0).UTC()
		mem.LastRecalled = &t
		out = append(out, mem)
	}

	return out, nil
}
