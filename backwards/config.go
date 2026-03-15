package backwards

import "time"

// https://github.com/concourse/concourse/blob/master/atc/config.go
type ImageResource struct {
	Source map[string]any `yaml:"source,omitempty"`
	Type   string         `yaml:"type,omitempty"`
}

type TaskConfigRun struct {
	Args []string `yaml:"args,omitempty"`
	Path string   `validate:"required"   yaml:"path,omitempty"`
	User string   `yaml:"user,omitempty"`
}

type Input struct {
	Name string `validate:"required" yaml:"name,omitempty"`
}

type Output struct {
	Name string `validate:"required" yaml:"name,omitempty"`
}

type Inputs []Input

type Outputs []Output

type ContainerLimits struct {
	CPU    int64 `yaml:"cpu,omitempty"`
	Memory int64 `yaml:"memory,omitempty"`
}

// Cache represents a cached path for task execution.
type Cache struct {
	Path string `validate:"required" yaml:"path,omitempty"`
}

type Caches []Cache

type TaskConfig struct {
	Caches          Caches            `yaml:"caches,omitempty"`
	ContainerLimits ContainerLimits   `yaml:"container_limits,omitempty"`
	Env             map[string]string `yaml:"env,omitempty"`
	ImageResource   ImageResource     `yaml:"image_resource,omitempty"`
	Inputs          Inputs            `yaml:"inputs,omitempty"`
	Outputs         Outputs           `yaml:"outputs,omitempty"`
	Platform        string            `validate:"oneof='linux' 'darwin' 'windows'" yaml:"platform,omitempty"`
	Run             *TaskConfigRun    `yaml:"run,omitempty"`
}

type GetConfig struct {
	Resource string            `yaml:"resource,omitempty"`
	Passed   []string          `yaml:"passed,omitempty"`
	Params   map[string]string `yaml:"params,omitempty"`
	Trigger  bool              `yaml:"trigger,omitempty"`
	Version  any               `yaml:"version,omitempty"` // "latest" | "every" | map[string]string (pinned)
}

// GetVersionMode returns the version mode: "latest", "every", or "pinned".
func (g *GetConfig) GetVersionMode() string {
	if g.Version == nil {
		return "latest"
	}

	if str, ok := g.Version.(string); ok {
		if str == "every" {
			return "every"
		}

		return "latest"
	}

	return "pinned"
}

// GetPinnedVersion returns the pinned version map, or nil if not pinned.
func (g *GetConfig) GetPinnedVersion() map[string]string {
	if m, ok := g.Version.(map[string]any); ok {
		result := make(map[string]string)

		for k, v := range m {
			if str, ok := v.(string); ok {
				result[k] = str
			}
		}

		return result
	}

	if m, ok := g.Version.(map[string]string); ok {
		return m
	}

	return nil
}

type PutConfig struct {
	Resource  string            `yaml:"resource,omitempty"`
	Params    map[string]string `yaml:"params,omitempty"`
	GetParams map[string]string `yaml:"get_params,omitempty"`
	Inputs    []string          `yaml:"inputs,omitempty"`
	NoGet     bool              `yaml:"no_get,omitempty"`
}

type AcrossVar struct {
	Var         string   `yaml:"var,omitempty"`
	Values      []string `yaml:"values,omitempty"`
	MaxInFlight int      `yaml:"max_in_flight,omitempty"`
}

// AgentLLMConfig configures language model generation parameters for an agent step.
type AgentLLMConfig struct {
	Temperature *float32 `yaml:"temperature,omitempty"`
	MaxTokens   int32    `yaml:"max_tokens,omitempty"`
}

// AgentThinkingConfig enables extended thinking for supported models.
// Budget sets the maximum thinking tokens (>= 1024).
// Level is Gemini-specific: low | medium | high | minimal.
type AgentThinkingConfig struct {
	Budget int32  `yaml:"budget"`
	Level  string `yaml:"level,omitempty"`
}

// AgentSafetyConfig maps harm category names to block thresholds.
// Keys: harassment, hate_speech, sexually_explicit, dangerous_content.
// Values: block_none, block_low_and_above, block_medium_and_above, block_only_high, off.
type AgentSafetyConfig map[string]string

// AgentContextGuardConfig enables context window management to prevent
// conversations from exceeding the model's context limit.
type AgentContextGuardConfig struct {
	Strategy  string `yaml:"strategy"`             // threshold | sliding_window
	MaxTurns  int    `yaml:"max_turns,omitempty"`  // sliding_window: compact after N turns
	MaxTokens int    `yaml:"max_tokens,omitempty"` // threshold: manual context window override
}

// AgentLimitsConfig configures hard limits that stop agent execution.
type AgentLimitsConfig struct {
	MaxTurns       int   `yaml:"max_turns,omitempty"`        // stop after N LLM responses (default: 50)
	MaxTotalTokens int32 `yaml:"max_total_tokens,omitempty"` // stop when cumulative tokens reach this
}

// AgentContextTask specifies a prior pipeline task whose output is pre-fetched
// into the agent's session as a synthetic tool result before the first turn.
type AgentContextTask struct {
	Name  string `yaml:"name"           json:"name"`
	Field string `yaml:"field,omitempty" json:"field,omitempty"` // stdout | stderr | both (default)
}

// AgentContextFile specifies a volume file whose contents are pre-read into the
// agent's session history before the first turn, saving a read_file tool call.
// Path is "mountname/relative/path" (e.g. "diff/pr.diff").
type AgentContextFile struct {
	Path     string `yaml:"path"                json:"path"`
	MaxBytes int    `yaml:"max_bytes,omitempty" json:"max_bytes,omitempty"`
}

// AgentContext configures which prior task outputs and file contents to inject
// into the agent's session history before the first turn.
type AgentContext struct {
	Tasks    []AgentContextTask `yaml:"tasks,omitempty"    json:"tasks,omitempty"`
	Files    []AgentContextFile `yaml:"files,omitempty"    json:"files,omitempty"`
	MaxBytes int                `yaml:"max_bytes,omitempty" json:"max_bytes,omitempty"`
}

type Step struct {
	Assert *struct {
		Code   *int   `yaml:"code,omitempty"`
		Stderr string `yaml:"stderr,omitempty"`
		Stdout string `yaml:"stdout,omitempty"`
	} `yaml:"assert,omitempty"`

	Task            string           `yaml:"task,omitempty"`
	Parallelism     int              `yaml:"parallelism,omitempty"`
	TaskConfig      *TaskConfig      `yaml:"config,omitempty"`
	ContainerLimits *ContainerLimits `yaml:"container_limits,omitempty"`
	File            string           `yaml:"file,omitempty"`
	Image           string           `yaml:"image,omitempty"`
	Privileged      bool             `yaml:"privileged,omitempty"`

	Agent  string `yaml:"agent,omitempty"`
	Prompt string `yaml:"prompt,omitempty"`
	Model  string `yaml:"model,omitempty"`

	PromptFile string `yaml:"prompt_file,omitempty"`

	AgentLLM          *AgentLLMConfig          `yaml:"llm,omitempty"`
	AgentThinking     *AgentThinkingConfig     `yaml:"thinking,omitempty"`
	AgentSafety       AgentSafetyConfig        `yaml:"safety,omitempty"`
	AgentContextGuard *AgentContextGuardConfig `yaml:"context_guard,omitempty"`
	AgentLimits       *AgentLimitsConfig       `yaml:"limits,omitempty"`
	AgentContext      *AgentContext            `yaml:"context,omitempty"`

	Get       string    `yaml:"get,omitempty"`
	GetConfig GetConfig `yaml:",inline,omitempty"`

	Put       string     `yaml:"put,omitempty"`
	PutConfig *PutConfig `yaml:",inline,omitempty"`

	Do        Steps `yaml:"do,omitempty"`
	Ensure    *Step `yaml:"ensure,omitempty"`
	OnAbort   *Step `yaml:"on_abort,omitempty"`
	OnError   *Step `yaml:"on_error,omitempty"`
	OnSuccess *Step `yaml:"on_success,omitempty"`
	OnFailure *Step `yaml:"on_failure,omitempty"`
	Try       Steps `yaml:"try,omitempty"`

	InParallel struct {
		Steps    Steps `yaml:"steps,omitempty"`
		Limit    int   `yaml:"limit,omitempty"`
		FailFast bool  `yaml:"fail_fast,omitempty"`
	} `yaml:"in_parallel,omitempty"`

	Across         []AcrossVar `yaml:"across,omitempty"`
	AcrossFailFast bool        `yaml:"fail_fast,omitempty"`

	Attempts int           `yaml:"attempts,omitempty"`
	Timeout  time.Duration `yaml:"timeout,omitempty"`
}

type Steps []Step

// WebhookTriggerConfig holds the filter expression and optional parameter
// extraction map for webhook-based job triggers.
type WebhookTriggerConfig struct {
	Filter string            `yaml:"filter,omitempty" json:"filter,omitempty"`
	Params map[string]string `yaml:"params,omitempty" json:"params,omitempty"`
}

// Triggers holds the set of trigger configurations for a job.
type Triggers struct {
	Webhook *WebhookTriggerConfig `yaml:"webhook,omitempty" json:"webhook,omitempty"`
}

type Job struct {
	Assert *struct {
		Execution []string `yaml:"execution,omitempty"`
	} `yaml:"assert,omitempty"`

	BuildLogRetention *struct {
		Builds int `yaml:"builds,omitempty"`
		Days   int `yaml:"days,omitempty"`
	} `yaml:"build_log_retention,omitempty"`

	Name           string        `validate:"required,min=3"      yaml:"name,omitempty"`
	Plan           Steps         `validate:"required,min=1,dive" yaml:"plan,omitempty"`
	MaxInFlight    int           `yaml:"max_in_flight,omitempty"`
	Public         bool          `yaml:"public,omitempty"`
	Ensure         *Step         `yaml:"ensure,omitempty"`
	OnAbort        *Step         `yaml:"on_abort,omitempty"`
	OnError        *Step         `yaml:"on_error,omitempty"`
	OnSuccess      *Step         `yaml:"on_success,omitempty"`
	OnFailure      *Step         `yaml:"on_failure,omitempty"`
	Timeout        time.Duration `yaml:"timeout,omitempty"`
	Triggers       *Triggers     `yaml:"triggers,omitempty"       json:"triggers,omitempty"`
	WebhookTrigger string        `yaml:"webhook_trigger,omitempty"`
}

type Jobs []Job

type ResourceType struct {
	Name   string         `validate:"required"     yaml:"name,omitempty"`
	Source map[string]any `yaml:"source,omitempty"`
	Type   string         `validate:"required"     yaml:"type,omitempty"`
}

type ResourceTypes []ResourceType

type Resource struct {
	Name   string         `validate:"required"     yaml:"name,omitempty"`
	Icon   string         `yaml:"icon,omitempty"`
	Source map[string]any `yaml:"source,omitempty"`
	Type   string         `validate:"required"     yaml:"type,omitempty"`
}

type Resources []Resource

type Config struct {
	Assert struct {
		Execution []string `yaml:"execution,omitempty"`
	} `yaml:"assert,omitempty"`
	MaxInFlight   int           `yaml:"max_in_flight,omitempty"`
	Jobs          Jobs          `validate:"required,min=1,dive" yaml:"jobs"`
	Resources     Resources     `yaml:"resources"`
	ResourceTypes ResourceTypes `yaml:"resource_types"`
}
