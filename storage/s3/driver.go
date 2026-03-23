package s3

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/jtarchie/pocketci/runtime/support"
	"github.com/jtarchie/pocketci/s3config"
	"github.com/jtarchie/pocketci/storage"
)

// S3 implements the storage.Driver interface using AWS S3 (or S3-compatible
// stores like MinIO) as the backend. All data is stored as JSON objects at
// hierarchical paths within the configured bucket and prefix.
type S3 struct {
	*s3config.Client
	namespace string
	logger    *slog.Logger
}

// Config holds the configuration for an S3 storage driver.
type Config struct {
	s3config.Config
}

// NewS3 creates a new S3-backed storage driver.
func NewS3(cfg Config, namespace string, logger *slog.Logger) (storage.Driver, error) {
	client, err := s3config.NewClient(context.Background(), &cfg.Config)
	if err != nil {
		return nil, err
	}

	return &S3{
		Client:    client,
		namespace: namespace,
		logger:    logger,
	}, nil
}

func (s *S3) Close() error {
	return nil
}

func (s *S3) taskKey(prefix string) string {
	return s.FullKey("tasks" + path.Clean("/"+s.namespace+"/"+prefix))
}

// Set stores a payload at the given prefix, merging with any existing payload
// (replicating SQLite's jsonb_patch upsert semantics).
func (s *S3) Set(ctx context.Context, prefix string, payload any) error {
	key := s.taskKey(prefix)

	incoming, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	existing, err := s.getJSON(ctx, key)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("failed to get existing task: %w", err)
	}

	if existing != nil {
		var incomingMap map[string]any
		if err := json.Unmarshal(incoming, &incomingMap); err != nil {
			return fmt.Errorf("failed to unmarshal incoming payload: %w", err)
		}

		for k, v := range incomingMap {
			existing[k] = v
		}

		merged, err := json.Marshal(existing)
		if err != nil {
			return fmt.Errorf("failed to marshal merged payload: %w", err)
		}

		incoming = merged
	}

	return s.putJSON(ctx, key, incoming)
}

func (s *S3) Get(ctx context.Context, prefix string) (storage.Payload, error) {
	key := s.taskKey(prefix)

	payload, err := s.getJSON(ctx, key)
	if err != nil {
		return nil, err
	}

	return payload, nil
}

func (s *S3) GetAll(ctx context.Context, prefix string, fields []string) (storage.Results, error) {
	if len(fields) == 0 {
		fields = []string{"status"}
	}

	keyPrefix := s.taskKey(prefix)

	keys, err := s.ListKeys(ctx, keyPrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to list tasks: %w", err)
	}

	sort.Strings(keys)

	var results storage.Results

	for _, key := range keys {
		payload, err := s.getJSON(ctx, key)
		if err != nil {
			continue
		}

		logicalPath := s.StripPrefix(key)
		logicalPath = strings.TrimPrefix(logicalPath, "tasks")

		if len(fields) != 1 || fields[0] != "*" {
			filtered := storage.Payload{}
			for _, f := range fields {
				if v, ok := payload[f]; ok {
					filtered[f] = v
				}
			}

			payload = filtered
		}

		results = append(results, storage.Result{
			ID:      0,
			Path:    logicalPath,
			Payload: payload,
		})
	}

	return results, nil
}

func (s *S3) UpdateStatusForPrefix(ctx context.Context, prefix string, matchStatuses []string, newStatus string) error {
	if len(matchStatuses) == 0 {
		return nil
	}

	keyPrefix := s.taskKey(prefix)

	keys, err := s.ListKeys(ctx, keyPrefix)
	if err != nil {
		return fmt.Errorf("failed to list tasks for status update: %w", err)
	}

	matchSet := make(map[string]bool, len(matchStatuses))
	for _, ms := range matchStatuses {
		matchSet[ms] = true
	}

	for _, key := range keys {
		payload, err := s.getJSON(ctx, key)
		if err != nil {
			continue
		}

		status, _ := payload["status"].(string)
		if !matchSet[status] {
			continue
		}

		payload["status"] = newStatus

		data, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("failed to marshal updated payload: %w", err)
		}

		if err := s.putJSON(ctx, key, data); err != nil {
			return fmt.Errorf("failed to update task status for key %q: %w", key, err)
		}
	}

	return nil
}

// ─── Pipeline CRUD ──────────────────────────────────────────────────────────

func (s *S3) SavePipeline(ctx context.Context, name, content, driver, contentType string) (*storage.Pipeline, error) {
	newID := support.PipelineID(name, content)
	now := time.Now().UTC()

	var storedID string

	existing, err := s.getPipeline(ctx, s.pipelineByNameKey(name))
	if err == nil && existing != nil {
		storedID = existing.ID

		if storedID != newID {
			_ = s.DeleteKey(ctx, s.pipelineByIDKey(storedID))
		}
	}

	if storedID == "" {
		storedID = newID
	}

	pipeline := &storage.Pipeline{
		ID:          storedID,
		Name:        name,
		Content:     content,
		ContentType: contentType,
		Driver:      driver,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if existing != nil {
		pipeline.CreatedAt = existing.CreatedAt
	}

	data, err := json.Marshal(pipeline)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal pipeline: %w", err)
	}

	if err := s.putJSON(ctx, s.pipelineByIDKey(storedID), data); err != nil {
		return nil, fmt.Errorf("failed to save pipeline by id: %w", err)
	}

	if err := s.putJSON(ctx, s.pipelineByNameKey(name), data); err != nil {
		return nil, fmt.Errorf("failed to save pipeline by name: %w", err)
	}

	return pipeline, nil
}

func (s *S3) GetPipeline(ctx context.Context, id string) (*storage.Pipeline, error) {
	return s.getPipeline(ctx, s.pipelineByIDKey(id))
}

func (s *S3) GetPipelineByName(ctx context.Context, name string) (*storage.Pipeline, error) {
	return s.getPipeline(ctx, s.pipelineByNameKey(name))
}

func (s *S3) DeletePipeline(ctx context.Context, id string) error {
	pipeline, err := s.GetPipeline(ctx, id)
	if err != nil {
		return err
	}

	if err := s.DeleteKey(ctx, s.pipelineByIDKey(id)); err != nil {
		return fmt.Errorf("failed to delete pipeline by id: %w", err)
	}

	if err := s.DeleteKey(ctx, s.pipelineByNameKey(pipeline.Name)); err != nil {
		return fmt.Errorf("failed to delete pipeline by name: %w", err)
	}

	runKeys, err := s.ListKeys(ctx, s.FullKey("runs/"))
	if err != nil {
		return nil
	}

	for _, key := range runKeys {
		run, err := s.getRun(ctx, key)
		if err != nil {
			continue
		}

		if run.PipelineID == id {
			_ = s.DeleteKey(ctx, key)
		}
	}

	return nil
}

// UpdatePipelineResumeEnabled updates the resume_enabled flag for a pipeline.
func (s *S3) UpdatePipelineResumeEnabled(ctx context.Context, pipelineID string, enabled bool) error {
	pipeline, err := s.GetPipeline(ctx, pipelineID)
	if err != nil {
		return err
	}

	pipeline.ResumeEnabled = enabled

	data, err := json.Marshal(pipeline)
	if err != nil {
		return fmt.Errorf("failed to marshal pipeline: %w", err)
	}

	if err := s.putJSON(ctx, s.pipelineByIDKey(pipelineID), data); err != nil {
		return fmt.Errorf("failed to update pipeline: %w", err)
	}

	return nil
}

// UpdatePipelinePaused updates the paused flag for a pipeline.
func (s *S3) UpdatePipelinePaused(ctx context.Context, pipelineID string, paused bool) error {
	pipeline, err := s.GetPipeline(ctx, pipelineID)
	if err != nil {
		return err
	}

	pipeline.Paused = paused

	data, err := json.Marshal(pipeline)
	if err != nil {
		return fmt.Errorf("failed to marshal pipeline: %w", err)
	}

	if err := s.putJSON(ctx, s.pipelineByIDKey(pipelineID), data); err != nil {
		return fmt.Errorf("failed to update pipeline: %w", err)
	}

	return nil
}

// UpdatePipelineRBACExpression updates the RBAC expression for a pipeline.
func (s *S3) UpdatePipelineRBACExpression(ctx context.Context, pipelineID, expression string) error {
	pipeline, err := s.GetPipeline(ctx, pipelineID)
	if err != nil {
		return err
	}

	pipeline.RBACExpression = expression

	data, err := json.Marshal(pipeline)
	if err != nil {
		return fmt.Errorf("failed to marshal pipeline: %w", err)
	}

	if err := s.putJSON(ctx, s.pipelineByIDKey(pipelineID), data); err != nil {
		return fmt.Errorf("failed to update pipeline: %w", err)
	}

	return nil
}

// ─── Pipeline Run operations ────────────────────────────────────────────────

func (s *S3) SaveRun(ctx context.Context, pipelineID string) (*storage.PipelineRun, error) {
	id := support.UniqueID()
	now := time.Now().UTC()

	run := &storage.PipelineRun{
		ID:         id,
		PipelineID: pipelineID,
		Status:     storage.RunStatusQueued,
		CreatedAt:  now,
	}

	data, err := json.Marshal(run)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal run: %w", err)
	}

	if err := s.putJSON(ctx, s.runKey(id), data); err != nil {
		return nil, fmt.Errorf("failed to save run: %w", err)
	}

	return run, nil
}

func (s *S3) GetRun(ctx context.Context, runID string) (*storage.PipelineRun, error) {
	return s.getRun(ctx, s.runKey(runID))
}

// GetRunsByStatus returns all pipeline runs with the given status.
func (s *S3) GetRunsByStatus(ctx context.Context, status storage.RunStatus) ([]storage.PipelineRun, error) {
	keys, err := s.ListKeys(ctx, s.runsPrefix())
	if err != nil {
		return nil, fmt.Errorf("failed to list runs: %w", err)
	}

	var runs []storage.PipelineRun

	for _, key := range keys {
		run, err := s.getRun(ctx, key)
		if err != nil {
			continue
		}

		if run.Status == status {
			runs = append(runs, *run)
		}
	}

	return runs, nil
}

// GetRunStats returns the count of runs grouped by status.
func (s *S3) GetRunStats(ctx context.Context) (map[storage.RunStatus]int, error) {
	keys, err := s.ListKeys(ctx, s.runsPrefix())
	if err != nil {
		return nil, fmt.Errorf("failed to list runs: %w", err)
	}

	stats := make(map[storage.RunStatus]int)

	for _, key := range keys {
		run, rErr := s.getRun(ctx, key)
		if rErr != nil {
			continue
		}
		stats[run.Status]++
	}

	return stats, nil
}

// GetRecentRunsByStatus returns the most recent N runs with the given status.
func (s *S3) GetRecentRunsByStatus(ctx context.Context, status storage.RunStatus, limit int) ([]storage.PipelineRun, error) {
	all, err := s.GetRunsByStatus(ctx, status)
	if err != nil {
		return nil, err
	}

	// Sort by CreatedAt descending (most recent first)
	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})

	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}

	return all, nil
}

func (s *S3) UpdateRunStatus(ctx context.Context, runID string, status storage.RunStatus, errorMessage string) error {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return err
	}

	// State machine: "failed" and "queued" can overwrite any state (stop/resume).
	// All other targets only apply when the current status is non-terminal.
	if run.Status.IsTerminal() && status != storage.RunStatusFailed && status != storage.RunStatusQueued {
		return nil // transition blocked — silent no-op
	}

	now := time.Now().UTC()
	run.Status = status

	switch status {
	case storage.RunStatusRunning:
		run.StartedAt = &now
	case storage.RunStatusSuccess, storage.RunStatusFailed, storage.RunStatusSkipped:
		run.CompletedAt = &now
		run.ErrorMessage = errorMessage
	}

	data, err := json.Marshal(run)
	if err != nil {
		return fmt.Errorf("failed to marshal updated run: %w", err)
	}

	return s.putJSON(ctx, s.runKey(runID), data)
}

func (s *S3) SearchRunsByPipeline(ctx context.Context, pipelineID, query string, page, perPage int) (*storage.PaginationResult[storage.PipelineRun], error) {
	if page < 1 {
		page = 1
	}

	if perPage < 1 {
		perPage = 20
	}

	runKeys, err := s.ListKeys(ctx, s.FullKey("runs/"))
	if err != nil {
		return emptyRunPage(page, perPage), nil
	}

	var matched []storage.PipelineRun

	for _, key := range runKeys {
		run, err := s.getRun(ctx, key)
		if err != nil {
			continue
		}

		if run.PipelineID != pipelineID {
			continue
		}

		if query != "" && !runMatchesQuery(run, query) {
			continue
		}

		matched = append(matched, *run)
	}

	sort.Slice(matched, func(i, j int) bool {
		return matched[i].CreatedAt.After(matched[j].CreatedAt)
	})

	return paginate(matched, page, perPage), nil
}

// PruneRunsByPipeline deletes old pipeline runs for a pipeline.
// If keepBuilds > 0, runs beyond the N most recent are deleted.
// If cutoffTime is non-nil, runs created before that time are deleted.
// Both constraints are applied independently.
func (s *S3) PruneRunsByPipeline(ctx context.Context, pipelineID string, keepBuilds int, cutoffTime *time.Time) error {
	keys, err := s.ListKeys(ctx, s.runsPrefix())
	if err != nil {
		return nil
	}

	var runs []storage.PipelineRun

	for _, key := range keys {
		run, err := s.getRun(ctx, key)
		if err != nil {
			continue
		}

		if run.PipelineID == pipelineID {
			runs = append(runs, *run)
		}
	}

	sort.Slice(runs, func(i, j int) bool {
		return runs[i].CreatedAt.After(runs[j].CreatedAt)
	})

	toDelete := map[string]bool{}

	if keepBuilds > 0 && len(runs) > keepBuilds {
		for _, r := range runs[keepBuilds:] {
			toDelete[r.ID] = true
		}
	}

	if cutoffTime != nil {
		for _, r := range runs {
			if r.CreatedAt.Before(*cutoffTime) {
				toDelete[r.ID] = true
			}
		}
	}

	for id := range toDelete {
		_ = s.DeleteKey(ctx, s.runKey(id))
	}

	return nil
}

// ─── Search operations ──────────────────────────────────────────────────────

func (s *S3) SearchPipelines(ctx context.Context, query string, page, perPage int) (*storage.PaginationResult[storage.Pipeline], error) {
	if page < 1 {
		page = 1
	}

	if perPage < 1 {
		perPage = 20
	}

	keys, err := s.ListKeys(ctx, s.FullKey("pipelines/by-id/"))
	if err != nil {
		return emptyPipelinePage(page, perPage), nil
	}

	var matched []storage.Pipeline

	for _, key := range keys {
		p, err := s.getPipeline(ctx, key)
		if err != nil {
			continue
		}

		if query == "" || pipelineMatchesQuery(p, query) {
			matched = append(matched, *p)
		}
	}

	sort.Slice(matched, func(i, j int) bool {
		return matched[i].CreatedAt.After(matched[j].CreatedAt)
	})

	return paginate(matched, page, perPage), nil
}

func (s *S3) Search(ctx context.Context, prefix, query string) (storage.Results, error) {
	if query == "" {
		return nil, nil
	}

	keyPrefix := s.taskKey(prefix)

	keys, err := s.ListKeys(ctx, keyPrefix)
	if err != nil {
		return nil, nil
	}

	lowerQuery := strings.ToLower(query)
	var results storage.Results

	for _, key := range keys {
		payload, err := s.getJSON(ctx, key)
		if err != nil {
			continue
		}

		logicalPath := s.StripPrefix(key)
		logicalPath = strings.TrimPrefix(logicalPath, "tasks")

		if pathOrPayloadMatches(logicalPath, payload, lowerQuery) {
			summary := storage.Payload{}
			if v, ok := payload["status"]; ok {
				summary["status"] = v
			}

			if v, ok := payload["elapsed"]; ok {
				summary["elapsed"] = v
			}

			if v, ok := payload["started_at"]; ok {
				summary["started_at"] = v
			}

			results = append(results, storage.Result{
				ID:      0,
				Path:    logicalPath,
				Payload: summary,
			})
		}
	}

	return results, nil
}

// ─── S3 key helpers ─────────────────────────────────────────────────────────

func (s *S3) pipelineByIDKey(id string) string {
	return s.FullKey("pipelines/by-id/" + id + ".json")
}

func (s *S3) pipelineByNameKey(name string) string {
	return s.FullKey("pipelines/by-name/" + name + ".json")
}

func (s *S3) runKey(id string) string {
	return s.FullKey("runs/" + id + ".json")
}

func (s *S3) runsPrefix() string {
	return s.FullKey("runs/")
}

// ─── S3 low-level helpers ───────────────────────────────────────────────────

func (s *S3) getJSON(ctx context.Context, key string) (storage.Payload, error) {
	data, err := s.GetBytes(ctx, key)
	if err != nil {
		if s3config.IsNotFound(err) {
			return nil, storage.ErrNotFound
		}

		return nil, err
	}

	var payload storage.Payload

	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("failed to unmarshal object %q: %w", key, err)
	}

	return payload, nil
}

func (s *S3) putJSON(ctx context.Context, key string, data []byte) error {
	return s.PutBytes(ctx, key, data, "application/json")
}

func (s *S3) getPipeline(ctx context.Context, key string) (*storage.Pipeline, error) {
	data, err := s.GetBytes(ctx, key)
	if err != nil {
		if s3config.IsNotFound(err) {
			return nil, storage.ErrNotFound
		}

		return nil, fmt.Errorf("failed to get pipeline %q: %w", key, err)
	}

	var pipeline storage.Pipeline

	if err := json.Unmarshal(data, &pipeline); err != nil {
		return nil, fmt.Errorf("failed to unmarshal pipeline %q: %w", key, err)
	}

	return &pipeline, nil
}

func (s *S3) getRun(ctx context.Context, key string) (*storage.PipelineRun, error) {
	data, err := s.GetBytes(ctx, key)
	if err != nil {
		if s3config.IsNotFound(err) {
			return nil, storage.ErrNotFound
		}

		return nil, fmt.Errorf("failed to get run %q: %w", key, err)
	}

	var run storage.PipelineRun

	if err := json.Unmarshal(data, &run); err != nil {
		return nil, fmt.Errorf("failed to unmarshal run %q: %w", key, err)
	}

	return &run, nil
}

// ─── Utility functions ──────────────────────────────────────────────────────

func runMatchesQuery(run *storage.PipelineRun, query string) bool {
	lower := strings.ToLower(query)

	return strings.Contains(strings.ToLower(run.ID), lower) ||
		strings.Contains(strings.ToLower(string(run.Status)), lower) ||
		strings.Contains(strings.ToLower(run.ErrorMessage), lower)
}

func pipelineMatchesQuery(p *storage.Pipeline, query string) bool {
	lower := strings.ToLower(query)

	return strings.Contains(strings.ToLower(p.Name), lower) ||
		strings.Contains(strings.ToLower(p.Content), lower)
}

func pathOrPayloadMatches(p string, payload storage.Payload, lowerQuery string) bool {
	if strings.Contains(strings.ToLower(p), lowerQuery) {
		return true
	}

	for _, v := range payload {
		str, ok := v.(string)
		if ok && strings.Contains(strings.ToLower(str), lowerQuery) {
			return true
		}
	}

	return false
}

func paginate[T any](items []T, page, perPage int) *storage.PaginationResult[T] {
	total := len(items)
	totalPages := (total + perPage - 1) / perPage

	if totalPages == 0 {
		totalPages = 1
	}

	offset := (page - 1) * perPage
	end := offset + perPage

	if offset > total {
		offset = total
	}

	if end > total {
		end = total
	}

	return &storage.PaginationResult[T]{
		Items:      items[offset:end],
		Page:       page,
		PerPage:    perPage,
		TotalItems: total,
		TotalPages: totalPages,
		HasNext:    page < totalPages,
	}
}

func emptyRunPage(page, perPage int) *storage.PaginationResult[storage.PipelineRun] {
	return &storage.PaginationResult[storage.PipelineRun]{
		Items:      []storage.PipelineRun{},
		Page:       page,
		PerPage:    perPage,
		TotalItems: 0,
		TotalPages: 1,
		HasNext:    false,
	}
}

func emptyPipelinePage(page, perPage int) *storage.PaginationResult[storage.Pipeline] {
	return &storage.PaginationResult[storage.Pipeline]{
		Items:      []storage.Pipeline{},
		Page:       page,
		PerPage:    perPage,
		TotalItems: 0,
		TotalPages: 1,
		HasNext:    false,
	}
}
