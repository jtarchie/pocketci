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

// ToolDef describes a tool available to the parent agent. Covers both
// agent tools (LLM sub-agents) and task tools (container commands).
// Distinguish by the IsTask flag.
type ToolDef struct {
	Name             string            `json:"name"`
	Prompt           string            `json:"prompt,omitempty"`
	Model            string            `json:"model,omitempty"`
	Image            string            `json:"image,omitempty"`
	StorageKeyPrefix string            `json:"storageKeyPrefix,omitempty"` // parent storage key for nested path persistence
	IsTask           bool              `json:"is_task,omitempty"`
	Description      string            `json:"description,omitempty"`
	CommandPath      string            `json:"command_path,omitempty"`
	CommandArgs      []string          `json:"command_args,omitempty"`
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
	ContextGuard     *AgentContextGuardConfig               `json:"context_guard,omitempty"`
	Limits           *AgentLimitsConfig                     `json:"limits,omitempty"`
	Context          *AgentContext                          `json:"context,omitempty"`
	Validation       *AgentValidationConfig                 `json:"validation,omitempty"`
	Tools            []ToolDef                              `json:"tools,omitempty"`
	OutputSchema     map[string]interface{}                 `json:"output_schema,omitempty"`
	ToolTimeout      string                                 `json:"tool_timeout,omitempty"`
	// OnOutput is called with streaming chunks. Not serialised from JS.
	OnOutput pipelinerunner.OutputCallback `json:"-"`
	// OnAuditEvent is called every time an audit event is appended.
	OnAuditEvent func(AuditEvent) `json:"-"`
	// OnUsage is called whenever cumulative usage changes.
	OnUsage func(AgentUsage) `json:"-"`
	// Internal fields populated by Runtime.Agent() — not exposed to JS.
	Storage     storage.Driver `json:"-"`
	Namespace   string         `json:"-"`
	RunID       string         `json:"-"`
	PipelineID  string         `json:"-"`
	TriggeredBy string         `json:"-"`
}
