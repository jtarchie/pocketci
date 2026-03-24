package storage

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrNotFound is returned when a requested key does not exist.
var ErrNotFound = errors.New("not found")

// ErrPipelinePaused is returned when a run is attempted on a paused pipeline.
var ErrPipelinePaused = errors.New("pipeline is paused")

// ContentType represents the format of a pipeline's content.
type ContentType = string

const (
	ContentTypeYAML       ContentType = "yaml"
	ContentTypeTypeScript ContentType = "ts"
	ContentTypeJavaScript ContentType = "js"
)

// Pipeline represents a stored pipeline definition.
type Pipeline struct {
	ID             string      `json:"id"`
	Name           string      `json:"name"`
	Content        string      `json:"content"`
	ContentType    ContentType `json:"content_type"`
	Driver         string      `json:"driver"`
	ResumeEnabled  bool        `json:"resume_enabled"`
	Paused         bool        `json:"paused"`
	RBACExpression string      `json:"rbac_expression,omitempty"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

// RunStatus represents the status of a pipeline run.
type RunStatus string

const (
	RunStatusQueued  RunStatus = "queued"
	RunStatusRunning RunStatus = "running"
	RunStatusSuccess RunStatus = "success"
	RunStatusFailed  RunStatus = "failed"
	RunStatusSkipped RunStatus = "skipped"
)

// IsTerminal returns true if the status represents a final state
// (success, failed, or skipped).
func (s RunStatus) IsTerminal() bool {
	return s == RunStatusSuccess || s == RunStatusFailed || s == RunStatusSkipped
}

// TriggerType represents what initiated a pipeline run.
type TriggerType string

const (
	TriggerTypeManual  TriggerType = "manual"
	TriggerTypeWebhook TriggerType = "webhook"
	TriggerTypeCLI     TriggerType = "cli"
	TriggerTypeResume  TriggerType = "resume"
)

// TriggerWebhookInput captures the full webhook HTTP request for audit and retrigger.
type TriggerWebhookInput struct {
	Provider  string            `json:"provider"`
	EventType string            `json:"eventType"`
	Method    string            `json:"method"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body,omitempty"`
	Query     map[string]string `json:"query,omitempty"`
}

// TriggerInput holds the input data that triggered a pipeline run,
// enabling retrigger with identical input.
type TriggerInput struct {
	Args    []string             `json:"args,omitempty"`
	Webhook *TriggerWebhookInput `json:"webhook,omitempty"`
}

// PipelineRun represents an execution of a pipeline.
type PipelineRun struct {
	ID           string       `json:"id"`
	PipelineID   string       `json:"pipeline_id"`
	Status       RunStatus    `json:"status"`
	TriggerType  TriggerType  `json:"trigger_type"`
	TriggeredBy  string       `json:"triggered_by,omitempty"`
	TriggerInput TriggerInput `json:"trigger_input"`
	StartedAt    *time.Time   `json:"started_at,omitempty"`
	CompletedAt  *time.Time   `json:"completed_at,omitempty"`
	ErrorMessage string       `json:"error_message,omitempty"`
	CreatedAt    time.Time    `json:"created_at"`
}

// PaginationResult holds paginated items along with pagination metadata.
type PaginationResult[T any] struct {
	Items      []T  `json:"items"`
	Page       int  `json:"page"`
	PerPage    int  `json:"per_page"`
	TotalItems int  `json:"total_items"`
	TotalPages int  `json:"total_pages"`
	HasNext    bool `json:"has_next"`
}

type Driver interface {
	Close() error
	Set(ctx context.Context, prefix string, payload any) error
	Get(ctx context.Context, prefix string) (Payload, error)
	GetAll(ctx context.Context, prefix string, fields []string) (Results, error)
	// UpdateStatusForPrefix sets the status field of all task records under prefix
	// whose current status matches one of matchStatuses to newStatus.
	// It uses the same jsonb_patch upsert semantics as Set, so only the status
	// field is overwritten and all other payload fields are preserved.
	UpdateStatusForPrefix(ctx context.Context, prefix string, matchStatuses []string, newStatus string) error

	// Pipeline CRUD operations
	SavePipeline(ctx context.Context, name, content, driver, contentType string) (*Pipeline, error)
	UpdatePipelineResumeEnabled(ctx context.Context, pipelineID string, enabled bool) error
	UpdatePipelinePaused(ctx context.Context, pipelineID string, paused bool) error
	UpdatePipelineRBACExpression(ctx context.Context, pipelineID, expression string) error
	GetPipeline(ctx context.Context, id string) (*Pipeline, error)
	GetPipelineByName(ctx context.Context, name string) (*Pipeline, error)
	DeletePipeline(ctx context.Context, id string) error

	// Pipeline run operations
	SaveRun(ctx context.Context, pipelineID string, triggerType TriggerType, triggeredBy string, triggerInput TriggerInput) (*PipelineRun, error)
	GetRun(ctx context.Context, runID string) (*PipelineRun, error)
	GetRunsByStatus(ctx context.Context, status RunStatus) ([]PipelineRun, error)
	// GetRunStats returns the count of runs grouped by status.
	GetRunStats(ctx context.Context) (map[RunStatus]int, error)
	// GetRecentRunsByStatus returns the most recent N runs with the given status.
	GetRecentRunsByStatus(ctx context.Context, status RunStatus, limit int) ([]PipelineRun, error)
	SearchRunsByPipeline(ctx context.Context, pipelineID, query string, page, perPage int) (*PaginationResult[PipelineRun], error)
	UpdateRunStatus(ctx context.Context, runID string, status RunStatus, errorMessage string) error
	// PruneRunsByPipeline deletes old pipeline runs according to retention limits.
	// keepBuilds: if > 0, delete runs beyond the N most recent.
	// cutoffTime: if non-nil, delete runs created before this time.
	// Both constraints are applied independently (a run is deleted if it exceeds either).
	PruneRunsByPipeline(ctx context.Context, pipelineID string, keepBuilds int, cutoffTime *time.Time) error

	// Full-text search operations
	//
	// SearchPipelines returns pipelines whose name or content match query using
	// FTS5. An empty query returns all pipelines.
	SearchPipelines(ctx context.Context, query string, page, perPage int) (*PaginationResult[Pipeline], error)

	// Search returns records whose indexed text matches query, scoped to paths
	// that begin with prefix. Set automatically indexes content on write so no
	// separate indexing step is required. prefix follows the same convention as
	// Set (no namespace; the implementation adds it internally).
	Search(ctx context.Context, prefix, query string) (Results, error)
}

type Payload map[string]any

func (p *Payload) Value() (driver.Value, error) {
	contents, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("could not marshal payload: %w", err)
	}

	return contents, nil
}

func (p *Payload) Scan(sqlValue any) error {
	switch typedValue := sqlValue.(type) {
	case string:
		err := json.NewDecoder(bytes.NewBufferString(typedValue)).Decode(p)
		if err != nil {
			return fmt.Errorf("could not unmarshal string payload: %w", err)
		}

		return nil
	case []byte:
		err := json.NewDecoder(bytes.NewBuffer(typedValue)).Decode(p)
		if err != nil {
			return fmt.Errorf("could not unmarshal byte payload: %w", err)
		}

		return nil
	case nil:
		return nil
	default:
		return fmt.Errorf("%w: cannot scan type %T: %v", errors.ErrUnsupported, sqlValue, sqlValue)
	}
}

type Result struct {
	ID      int     `db:"id"      json:"id"`
	Path    string  `db:"path"    json:"path"`
	Payload Payload `db:"payload" json:"payload"`
}

type Results []Result

func (results Results) AsTree() *Tree[Payload] {
	tree := NewTree[Payload]()
	for _, result := range results {
		tree.AddNode(result.Path, result.Payload)
	}

	tree.Flatten()

	return tree
}
