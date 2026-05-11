package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/georgysavva/scany/v2/sqlscan"
	"github.com/jtarchie/pocketci/storage"
	"github.com/samber/lo"
)

// knownSQLiteColumns lists schema columns promoted out of the JSON payload.
// Must be kept sorted for binary search in isKnownColumn.
var knownSQLiteColumns = []string{"elapsed", "error_message", "error_type", "started_at", "status"}

func isKnownColumn(field string) bool {
	i := sort.SearchStrings(knownSQLiteColumns, field)
	return i < len(knownSQLiteColumns) && knownSQLiteColumns[i] == field
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

// escapeLike escapes SQL LIKE wildcard characters (%, _, \) in a string
// so it can be used safely in a LIKE pattern with ESCAPE '\'.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)

	return s
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
		jsonSelects := strings.Join(
			lo.Map(fields, func(field string, _ int) string {
				if isKnownColumn(field) {
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
