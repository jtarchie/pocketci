// Package filter evaluates webhook trigger expressions against webhook data.
// It uses github.com/expr-lang/expr for safe, sandboxed boolean expressions.
package filter

import (
	"encoding/json"
	"fmt"

	"github.com/expr-lang/expr"
	"github.com/jtarchie/pocketci/runtime/jsapi"
)

// WebhookEnv is the expression environment exposing webhook metadata.
// All fields are available by name in filter expressions.
type WebhookEnv struct {
	Provider  string            `expr:"provider"`
	EventType string            `expr:"eventType"`
	Method    string            `expr:"method"`
	Headers   map[string]string `expr:"headers"`
	Query     map[string]string `expr:"query"`
	Body      string            `expr:"body"`
	// Payload holds the JSON-decoded body. Nil when the body is not valid JSON.
	Payload map[string]any `expr:"payload"`
}

// Evaluate compiles and runs a boolean expression against env.
// Returns (false, error) when the expression is invalid or evaluation fails.
func Evaluate(expression string, env WebhookEnv) (bool, error) {
	program, err := expr.Compile(expression, expr.Env(env), expr.AsBool())
	if err != nil {
		return false, fmt.Errorf("webhook_trigger compile error: %w", err)
	}

	result, err := expr.Run(program, env)
	if err != nil {
		return false, fmt.Errorf("webhook_trigger eval error: %w", err)
	}

	boolResult, ok := result.(bool)
	if !ok {
		return false, fmt.Errorf("webhook_trigger expected bool result, got %T", result)
	}

	return boolResult, nil
}

// EvaluateString compiles and runs a string-valued expression against env.
// Non-string results are converted via fmt.Sprintf. Used for triggers.webhook.params.
func EvaluateString(expression string, env WebhookEnv) (string, error) {
	program, err := expr.Compile(expression, expr.Env(env))
	if err != nil {
		return "", fmt.Errorf("webhook_params compile error: %w", err)
	}

	result, err := expr.Run(program, env)
	if err != nil {
		return "", fmt.Errorf("webhook_params eval error: %w", err)
	}

	if s, ok := result.(string); ok {
		return s, nil
	}

	return fmt.Sprintf("%v", result), nil
}

// BuildWebhookEnv constructs a WebhookEnv from webhook data.
// The Body field is JSON-decoded into Payload when possible.
func BuildWebhookEnv(wd *jsapi.WebhookData) WebhookEnv {
	env := WebhookEnv{
		Provider:  wd.Provider,
		EventType: wd.EventType,
		Method:    wd.Method,
		Headers:   wd.Headers,
		Query:     wd.Query,
		Body:      wd.Body,
	}

	var payload map[string]any
	jsonErr := json.Unmarshal([]byte(wd.Body), &payload)
	if jsonErr == nil {
		env.Payload = payload
	}

	return env
}
