package backwards

import (
	"time"

	agent "github.com/jtarchie/pocketci/runtime/agent"
	"github.com/jtarchie/pocketci/runtime/jsapi"
)

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
	Image           string            `yaml:"image,omitempty"`
	ImageResource   ImageResource     `yaml:"image_resource,omitempty"`
	Inputs          Inputs            `yaml:"inputs,omitempty"`
	Outputs         Outputs           `yaml:"outputs,omitempty"`
	Platform        string            `validate:"oneof='linux' 'darwin' 'windows'" yaml:"platform,omitempty"`
	Run             *TaskConfigRun    `yaml:"run,omitempty"`
}

type GetConfig struct {
	Resource string   `yaml:"resource,omitempty"`
	Passed   []string `yaml:"passed,omitempty"`
	Trigger  bool     `yaml:"trigger,omitempty"`
	Version  any      `yaml:"version,omitempty"` // "latest" | "every" | map[string]string (pinned)
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
	GetParams map[string]string `yaml:"get_params,omitempty"`
	Inputs    []string          `yaml:"inputs,omitempty"`
	NoGet     bool              `yaml:"no_get,omitempty"`
}

type AcrossVar struct {
	Var         string   `yaml:"var,omitempty"`
	Values      []string `yaml:"values,omitempty"`
	MaxInFlight int      `yaml:"max_in_flight,omitempty"`
}

// AgentSafetyConfig maps harm category names to block thresholds.
type AgentSafetyConfig = map[string]string

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
	URI             string           `yaml:"uri,omitempty"`
	Image           string           `yaml:"image,omitempty"`
	Privileged      bool             `yaml:"privileged,omitempty"`

	Agent  string `yaml:"agent,omitempty"`
	Prompt string `yaml:"prompt,omitempty"`
	Model  string `yaml:"model,omitempty"`

	PromptFile string `yaml:"prompt_file,omitempty"`

	AgentLLM          *agent.AgentLLMConfig          `yaml:"llm,omitempty"`
	AgentThinking     *agent.AgentThinkingConfig     `yaml:"thinking,omitempty"`
	AgentSafety       AgentSafetyConfig              `yaml:"safety,omitempty"`
	AgentContextGuard *agent.AgentContextGuardConfig `yaml:"context_guard,omitempty"`
	AgentLimits       *agent.AgentLimitsConfig       `yaml:"limits,omitempty"`
	AgentContext      *agent.AgentContext            `yaml:"context,omitempty"`
	AgentValidation   *agent.AgentValidationConfig   `yaml:"validation,omitempty"`
	AgentToolTimeout  string                         `yaml:"tool_timeout,omitempty"`
	Tools             Steps                          `yaml:"tools,omitempty"`

	Get       string    `yaml:"get,omitempty"`
	GetConfig GetConfig `yaml:",inline,omitempty"`

	Put       string     `yaml:"put,omitempty"`
	PutConfig *PutConfig `yaml:",inline,omitempty"`

	// Params is shared by get and put steps. It lives on Step (not in the
	// inline GetConfig/PutConfig) to avoid duplicate YAML keys during
	// marshal/unmarshal round-trips.
	Params map[string]string `yaml:"params,omitempty"`

	Notify  any    `yaml:"notify,omitempty"`  // string or []string — notification config name(s)
	Message string `yaml:"message,omitempty"` // Go template message with Sprig functions
	Async   bool   `yaml:"async,omitempty"`   // fire-and-forget mode

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

// NotifyNames returns the notification config names as a string slice.
// Handles both single-string and list-of-strings YAML representations.
func (s *Step) NotifyNames() []string {
	switch v := s.Notify.(type) {
	case string:
		if v == "" {
			return nil
		}

		return []string{v}
	case []any:
		names := make([]string, 0, len(v))

		for _, item := range v {
			if str, ok := item.(string); ok {
				names = append(names, str)
			}
		}

		return names
	case []string:
		return v
	default:
		return nil
	}
}

// WebhookTriggerConfig holds the filter expression and optional parameter
// extraction map for webhook-based job triggers.
type WebhookTriggerConfig struct {
	Filter   string            `json:"filter,omitempty"    yaml:"filter,omitempty"`
	DedupKey string            `json:"dedup_key,omitempty" yaml:"dedup_key,omitempty"`
	Params   map[string]string `json:"params,omitempty"    yaml:"params,omitempty"`
}

// ScheduleTriggerConfig defines a schedule for automatic job triggering.
// Exactly one of Cron or Every must be set.
type ScheduleTriggerConfig struct {
	Cron  string `json:"cron,omitempty"  yaml:"cron,omitempty"`
	Every string `json:"every,omitempty" yaml:"every,omitempty"`
}

// Triggers holds the set of trigger configurations for a job.
type Triggers struct {
	Webhook  *WebhookTriggerConfig  `json:"webhook,omitempty"  yaml:"webhook,omitempty"`
	Schedule *ScheduleTriggerConfig `json:"schedule,omitempty" yaml:"schedule,omitempty"`
}

// GateConfig defines an approval gate on a job.
// The pipeline pauses before executing the job's plan until the gate is approved.
type GateConfig struct {
	Message string `json:"message,omitempty" yaml:"message,omitempty"`
	Timeout string `json:"timeout,omitempty" yaml:"timeout,omitempty"`
}

type Job struct {
	Assert *struct {
		Execution []string `yaml:"execution,omitempty"`
	} `yaml:"assert,omitempty"`

	BuildLogRetention *struct {
		Builds int `yaml:"builds,omitempty"`
		Days   int `yaml:"days,omitempty"`
	} `yaml:"build_log_retention,omitempty"`

	Name           string        `validate:"required,min=3"        yaml:"name,omitempty"`
	Plan           Steps         `validate:"required,min=1,dive"   yaml:"plan,omitempty"`
	DependsOn      []string      `json:"depends_on,omitempty"      yaml:"depends_on,omitempty"`
	Gate           *GateConfig   `json:"gate,omitempty"            yaml:"gate,omitempty"`
	MaxInFlight    int           `yaml:"max_in_flight,omitempty"`
	Public         bool          `yaml:"public,omitempty"`
	Ensure         *Step         `yaml:"ensure,omitempty"`
	OnAbort        *Step         `yaml:"on_abort,omitempty"`
	OnError        *Step         `yaml:"on_error,omitempty"`
	OnSuccess      *Step         `yaml:"on_success,omitempty"`
	OnFailure      *Step         `yaml:"on_failure,omitempty"`
	Timeout        time.Duration `yaml:"timeout,omitempty"`
	Triggers       *Triggers     `json:"triggers,omitempty"        yaml:"triggers,omitempty"`
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
	MaxInFlight   int                           `yaml:"max_in_flight,omitempty"`
	Notifications map[string]jsapi.NotifyConfig `yaml:"notifications,omitempty"`
	Jobs          Jobs                          `validate:"required,min=1,dive" yaml:"jobs"`
	Resources     Resources                     `yaml:"resources"`
	ResourceTypes ResourceTypes                 `yaml:"resource_types"`
}
