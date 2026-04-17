package agent

import (
	agentmodel "github.com/jtarchie/pocketci/runtime/agent/model"
	pipelinerunner "github.com/jtarchie/pocketci/runtime/runner"
	"github.com/jtarchie/pocketci/storage"
)

// AgentLLMConfig controls LLM generation parameters.
type AgentLLMConfig = agentmodel.LLMConfig

// AgentThinkingConfig enables extended thinking for supported models.
type AgentThinkingConfig = agentmodel.ThinkingConfig

// DefaultBaseURLs maps providers to their base URLs.
var DefaultBaseURLs = agentmodel.DefaultBaseURLs

// AgentMemoryConfig enables cross-run agent memory. When Enabled, the agent
// gains recall_memory and save_memory tools scoped by pipeline + agent name.
type AgentMemoryConfig struct {
	Enabled         bool `json:"enabled"                     yaml:"enabled"`
	MaxRecall       int  `json:"max_recall,omitempty"        yaml:"max_recall,omitempty"`
	MaxContentBytes int  `json:"max_content_bytes,omitempty" yaml:"max_content_bytes,omitempty"`
}

// ToolDef describes a tool available to the parent agent. Covers both
// agent tools (LLM sub-agents) and task tools (container commands).
// Distinguish by the IsTask flag.
type ToolDef struct {
	Name             string            `json:"name"`
	Prompt           string            `json:"prompt,omitempty"`
	Model            string            `json:"model,omitempty"`
	Image            string            `json:"image,omitempty"`
	StorageKeyPrefix string            `json:"storageKeyPrefix,omitempty"` // parent storage key for nested path persistence
	IsTask           bool              `json:"isTask,omitempty"`
	Description      string            `json:"description,omitempty"`
	CommandPath      string            `json:"commandPath,omitempty"`
	CommandArgs      []string          `json:"commandArgs,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
}

// AgentConfig is the configuration passed from JavaScript to runtime.agent().
type AgentConfig struct {
	Name             string                                 `json:"name"`
	Prompt           string                                 `json:"prompt"`
	Model            string                                 `json:"model"`
	Image            string                                 `json:"image"`
	Mounts           map[string]pipelinerunner.VolumeResult `json:"mounts"`
	OutputVolumePath string                                 `json:"outputVolumePath"`
	LLM              *AgentLLMConfig                        `json:"llm,omitempty"`
	Thinking         *AgentThinkingConfig                   `json:"thinking,omitempty"`
	Safety           map[string]string                      `json:"safety,omitempty"`
	ContextGuard     *AgentContextGuardConfig               `json:"contextGuard,omitempty"`
	Limits           *AgentLimitsConfig                     `json:"limits,omitempty"`
	Context          *AgentContext                          `json:"context,omitempty"`
	Validation       *AgentValidationConfig                 `json:"validation,omitempty"`
	Tools            []ToolDef                              `json:"tools,omitempty"`
	OutputSchema     map[string]interface{}                 `json:"outputSchema,omitempty"`
	ToolTimeout      string                                 `json:"toolTimeout,omitempty"`
	Memory           *AgentMemoryConfig                     `json:"memory,omitempty"`
	// OnOutput is called with streaming chunks. Not serialised from JS.
	OnOutput pipelinerunner.OutputCallback `json:"-"`
	// OnAuditEvent is called every time an audit event is appended.
	OnAuditEvent func(AuditEvent) `json:"-"`
	// OnUsage is called whenever cumulative usage changes.
	OnUsage func(AgentUsage) `json:"-"`
	// Internal fields populated by Runtime.Agent() — not exposed to JS.
	Storage          storage.Driver    `json:"-"`
	Namespace        string            `json:"-"`
	RunID            string            `json:"-"`
	PipelineID       string            `json:"-"`
	TriggeredBy      string            `json:"-"`
	BaseURLOverrides map[string]string `json:"-"` // overrides DefaultBaseURLs; used in tests to avoid global state
}
