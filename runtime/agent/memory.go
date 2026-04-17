package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

const (
	defaultMemoryMaxRecall       = 5
	defaultMemoryMaxContentBytes = 4096
)

type recallMemoryInput struct {
	Query string   `json:"query,omitempty"`
	Tags  []string `json:"tags,omitempty"`
	Limit int      `json:"limit,omitempty"`
}

type recalledMemory struct {
	Content      string   `json:"content"`
	Tags         []string `json:"tags"`
	CreatedAt    string   `json:"created_at"`
	RecallCount  int64    `json:"recall_count"`
	LastRecalled string   `json:"last_recalled,omitempty"`
}

type recallMemoryOutput struct {
	Memories []recalledMemory `json:"memories"`
}

type saveMemoryInput struct {
	Content string   `json:"content"`
	Tags    []string `json:"tags,omitempty"`
}

type saveMemoryOutput struct {
	Saved   bool `json:"saved"`
	Deduped bool `json:"deduped"`
}

// memoryAvailable reports whether the agent has enough context to use memory.
func memoryAvailable(config AgentConfig) bool {
	return config.Memory != nil &&
		config.Memory.Enabled &&
		config.Storage != nil &&
		config.PipelineID != "" &&
		config.Name != ""
}

func memoryLimits(config AgentConfig) (maxRecall, maxContentBytes int) {
	maxRecall = defaultMemoryMaxRecall
	maxContentBytes = defaultMemoryMaxContentBytes
	if config.Memory != nil {
		if config.Memory.MaxRecall > 0 {
			maxRecall = config.Memory.MaxRecall
		}
		if config.Memory.MaxContentBytes > 0 {
			maxContentBytes = config.Memory.MaxContentBytes
		}
	}
	return
}

func newRecallMemoryTool(ctx context.Context, config AgentConfig) (adktool.Tool, error) {
	return functiontool.New[recallMemoryInput, recallMemoryOutput](
		functiontool.Config{
			Name:        "recall_memory",
			Description: "Search prior lessons this agent saved in earlier runs of the same pipeline. Returns ranked results by relevance to query and tags. Call this before acting to reuse known solutions.",
		},
		func(_ adktool.Context, input recallMemoryInput) (recallMemoryOutput, error) {
			if !memoryAvailable(config) {
				return recallMemoryOutput{}, errors.New("agent memory not available")
			}

			maxRecall, _ := memoryLimits(config)
			limit := input.Limit
			if limit <= 0 || limit > maxRecall {
				limit = maxRecall
			}

			memories, err := config.Storage.RecallAgentMemories(
				ctx, config.PipelineID, config.Name, input.Query, input.Tags, limit,
			)
			if err != nil {
				return recallMemoryOutput{}, fmt.Errorf("recall_memory: %w", err)
			}

			out := recallMemoryOutput{Memories: make([]recalledMemory, 0, len(memories))}
			for _, m := range memories {
				item := recalledMemory{
					Content:     m.Content,
					Tags:        m.Tags,
					CreatedAt:   m.CreatedAt.Format(time.RFC3339),
					RecallCount: m.RecallCount,
				}
				if m.LastRecalled != nil {
					item.LastRecalled = m.LastRecalled.Format(time.RFC3339)
				}
				out.Memories = append(out.Memories, item)
			}

			return out, nil
		},
	)
}

func newSaveMemoryTool(ctx context.Context, config AgentConfig) (adktool.Tool, error) {
	return functiontool.New[saveMemoryInput, saveMemoryOutput](
		functiontool.Config{
			Name:        "save_memory",
			Description: "Persist a short, durable lesson so future runs of this pipeline can recall it. Use for conventions, known fixes, pipeline-specific facts. Identical content is deduplicated.",
		},
		func(_ adktool.Context, input saveMemoryInput) (saveMemoryOutput, error) {
			if !memoryAvailable(config) {
				return saveMemoryOutput{}, errors.New("agent memory not available")
			}

			if input.Content == "" {
				return saveMemoryOutput{}, errors.New("content is required")
			}

			_, maxContentBytes := memoryLimits(config)
			if len(input.Content) > maxContentBytes {
				return saveMemoryOutput{}, fmt.Errorf("content exceeds max %d bytes", maxContentBytes)
			}

			_, deduped, err := config.Storage.SaveAgentMemory(
				ctx, config.PipelineID, config.Name, input.Content, input.Tags,
			)
			if err != nil {
				return saveMemoryOutput{}, fmt.Errorf("save_memory: %w", err)
			}

			return saveMemoryOutput{Saved: true, Deduped: deduped}, nil
		},
	)
}

// buildMemoryTools returns the recall/save memory tools when the agent config
// enables memory. Returns an empty slice (no error) when memory is disabled or
// prerequisites are missing — memory is strictly opt-in.
func buildMemoryTools(ctx context.Context, config AgentConfig) ([]adktool.Tool, error) {
	if !memoryAvailable(config) {
		return nil, nil
	}

	recall, err := newRecallMemoryTool(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to create recall_memory tool: %w", err)
	}

	save, err := newSaveMemoryTool(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to create save_memory tool: %w", err)
	}

	return []adktool.Tool{recall, save}, nil
}
