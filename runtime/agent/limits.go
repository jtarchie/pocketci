package agent

import (
	"fmt"
	"strings"

	"github.com/achetronic/adk-utils-go/plugin/contextguard"
)

const (
	DefaultContextGuardMaxTurns  = 30
	DefaultContextGuardMaxTokens = 128000
	DefaultLimitsMaxTurns        = 50
	limitWarningTurnsBefore      = 2
)

// AgentContextGuardConfig enables context window management.
type AgentContextGuardConfig struct {
	Strategy  string `json:"strategy"             yaml:"strategy"`
	MaxTurns  int    `json:"max_turns,omitempty"  yaml:"max_turns,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty" yaml:"max_tokens,omitempty"`
}

// AgentLimitsConfig configures hard limits that stop agent execution.
type AgentLimitsConfig struct {
	MaxTurns       int   `json:"max_turns,omitempty"        yaml:"max_turns,omitempty"`
	MaxTotalTokens int32 `json:"max_total_tokens,omitempty" yaml:"max_total_tokens,omitempty"`
}

// normalizeContextGuardConfig normalises context guard configuration so limits
// are always applied deterministically when a context guard block is provided.
func NormalizeContextGuardConfig(cg *AgentContextGuardConfig) (string, int, error) {
	if cg == nil {
		return "", 0, nil
	}

	strategy := strings.ToLower(strings.TrimSpace(cg.Strategy))
	if strategy == "" {
		if cg.MaxTurns > 0 {
			strategy = "sliding_window"
		} else {
			strategy = "threshold"
		}
	}

	switch strategy {
	case "sliding_window":
		maxTurns := cg.MaxTurns
		if maxTurns <= 0 {
			maxTurns = DefaultContextGuardMaxTurns
		}

		return strategy, maxTurns, nil
	case "threshold":
		maxTokens := cg.MaxTokens
		if maxTokens <= 0 {
			maxTokens = DefaultContextGuardMaxTokens
		}

		return strategy, maxTokens, nil
	default:
		return "", 0, fmt.Errorf("invalid context_guard strategy %q: expected \"threshold\" or \"sliding_window\"", cg.Strategy)
	}
}

func resolveContextGuardOptions(cg *AgentContextGuardConfig) ([]contextguard.AgentOption, error) {
	strategy, value, err := NormalizeContextGuardConfig(cg)
	if err != nil {
		return nil, err
	}

	if strategy == "" {
		return nil, nil
	}

	switch strategy {
	case "sliding_window":
		return []contextguard.AgentOption{contextguard.WithSlidingWindow(value)}, nil
	case "threshold":
		return []contextguard.AgentOption{contextguard.WithMaxTokens(value)}, nil
	default:
		// normalizeContextGuardConfig already validates this branch.
		return nil, fmt.Errorf("unsupported context_guard strategy %q", strategy)
	}
}

// effectiveLimits returns the hard turn and token limits that apply. If no
// limits are configured, a sensible default max_turns is used to prevent
// runaway agents.
func EffectiveLimits(cfg *AgentLimitsConfig) (maxTurns int, maxTotalTokens int32) {
	if cfg == nil {
		return DefaultLimitsMaxTurns, 0
	}

	maxTurns = cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = DefaultLimitsMaxTurns
	}

	return maxTurns, cfg.MaxTotalTokens
}
