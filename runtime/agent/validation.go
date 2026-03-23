package agent

import (
	"fmt"

	"github.com/expr-lang/expr"
)

// AgentValidationConfig configures output validation via an Expr expression.
// The expression is evaluated with {text: string, status: string} as the environment.
// If it returns false, a follow-up prompt is sent asking the model to correct its output.
type AgentValidationConfig struct {
	Expr   string `json:"expr"             yaml:"expr"`
	Prompt string `json:"prompt,omitempty" yaml:"prompt,omitempty"`
}

// evalValidation compiles and runs a boolean Expr expression against the given
// environment. Returns (true, nil) when the expression passes.
func evalValidation(expression string, env map[string]any) (bool, error) {
	program, err := expr.Compile(expression, expr.Env(env), expr.AsBool())
	if err != nil {
		return false, fmt.Errorf("validation compile: %w", err)
	}

	result, err := expr.Run(program, env)
	if err != nil {
		return false, fmt.Errorf("validation eval: %w", err)
	}

	return result.(bool), nil //nolint:forcetypeassert
}
