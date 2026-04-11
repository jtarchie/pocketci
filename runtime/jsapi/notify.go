package jsapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"text/template"

	"github.com/dop251/goja"
	sprig "github.com/go-task/slim-sprig/v3"
	"github.com/jtarchie/pocketci/runtime/support"
	"github.com/jtarchie/pocketci/secrets"
	"github.com/nikoksr/notify"
)

// Adapter is implemented by each notification backend.
// Add new backends by creating a subpackage under runtime/jsapi/notifiers/
// and passing New() instances to NewNotifier.
type Adapter interface {
	Name() string
	Configure(notifier *notify.Notify, config NotifyConfig) error
}

// NotifyConfig represents the configuration for a notification backend.
type NotifyConfig struct {
	Type       string            `json:"type"`       // slack, teams, http, discord, smtp
	Token      string            `json:"token"`      // For Slack/Discord (bot token)
	Webhook    string            `json:"webhook"`    // For Teams
	URL        string            `json:"url"`        // For HTTP
	Channels   []string          `json:"channels"`   // For Slack/Discord
	Headers    map[string]string `json:"headers"`    // For HTTP
	Method     string            `json:"method"`     // For HTTP (defaults to POST)
	Recipients []string          `json:"recipients"` // Generic recipients / SMTP to-addresses
	SMTPHost   string            `json:"smtpHost"`   // For SMTP (e.g. "smtp.gmail.com:587")
	From       string            `json:"from"`       // For SMTP sender address
	Username   string            `json:"username"`   // For SMTP auth username
}

// NotifyContext provides metadata about the current pipeline execution for template rendering.
type NotifyContext struct {
	PipelineName string            `json:"pipelineName"`
	JobName      string            `json:"jobName"`
	BuildID      string            `json:"buildID"`
	Status       string            `json:"status"` // pending, running, success, failure, error
	StartTime    string            `json:"startTime"`
	EndTime      string            `json:"endTime"`
	Duration     string            `json:"duration"`
	Environment  map[string]string `json:"environment"`
	TaskResults  map[string]any    `json:"taskResults"`
}

// NotifyInput is the input for sending a notification from JavaScript.
type NotifyInput struct {
	Name    string `json:"name"`    // Config name (for named configs)
	Message string `json:"message"` // Template message
	Async   bool   `json:"async"`   // Fire-and-forget mode
}

// SendMultipleInput is the input for sending to multiple notification backends.
type SendMultipleInput struct {
	Names   []string `json:"names"`
	Message string   `json:"message"`
	Async   bool     `json:"async"`
}

// NotifyResult is the result of a notification attempt.
type NotifyResult struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// Notifier handles notification sending with configuration management.
type Notifier struct {
	configs        map[string]NotifyConfig
	adapters       map[string]Adapter
	context        NotifyContext
	logger         *slog.Logger
	mu             sync.RWMutex
	Disabled       bool
	secretsManager secrets.Manager
	pipelineID     string
}

// NewNotifier creates a new Notifier instance.
func NewNotifier(logger *slog.Logger, adapters []Adapter) *Notifier {
	registry := make(map[string]Adapter, len(adapters))
	for _, a := range adapters {
		registry[a.Name()] = a
	}

	return &Notifier{
		configs:  make(map[string]NotifyConfig),
		adapters: registry,
		logger:   logger.WithGroup("notifier.send"),
	}
}

// SetSecretsManager configures the notifier to resolve "secret:<KEY>"
// references in notification config fields (Token, Webhook, URL, Headers)
// before each send.
func (n *Notifier) SetSecretsManager(mgr secrets.Manager, pipelineID string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.secretsManager = mgr
	n.pipelineID = pipelineID
}

// resolveConfigSecrets returns a copy of config with all "secret:<KEY>"
// references in Token, Webhook, URL, and Headers resolved.
func (n *Notifier) resolveConfigSecrets(ctx context.Context, config NotifyConfig) (NotifyConfig, error) {
	n.mu.RLock()
	mgr := n.secretsManager
	pipelineID := n.pipelineID
	n.mu.RUnlock()

	if mgr == nil {
		return config, nil
	}

	scalarFields := []struct {
		name  string
		field *string
	}{
		{"token", &config.Token},
		{"webhook", &config.Webhook},
		{"url", &config.URL},
		{"username", &config.Username},
	}

	for _, f := range scalarFields {
		resolved, _, err := support.ResolveSecretString(ctx, mgr, pipelineID, *f.field)
		if err != nil {
			return config, fmt.Errorf("notification field %q: %w", f.name, err)
		}

		*f.field = resolved
	}

	// Resolve headers.
	if len(config.Headers) > 0 {
		resolvedHeaders := make(map[string]string, len(config.Headers))
		for k, v := range config.Headers {
			resolved, _, err := support.ResolveSecretString(ctx, mgr, pipelineID, v)
			if err != nil {
				return config, fmt.Errorf("notification header %q: %w", k, err)
			}

			resolvedHeaders[k] = resolved
		}

		config.Headers = resolvedHeaders
	}

	return config, nil
}

// SetConfigs sets the notification configurations.
func (n *Notifier) SetConfigs(configs map[string]NotifyConfig) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.configs = configs
}

// GetConfig returns a notification config by name.
func (n *Notifier) GetConfig(name string) (NotifyConfig, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	config, exists := n.configs[name]
	return config, exists
}

// SetContext sets the current pipeline context for template rendering.
func (n *Notifier) SetContext(ctx NotifyContext) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.context = ctx
}

// UpdateContext updates specific fields of the context.
func (n *Notifier) UpdateContext(updates func(*NotifyContext)) {
	n.mu.Lock()
	defer n.mu.Unlock()

	updates(&n.context)
}

// safeFuncMap returns a Sprig FuncMap with environment-access functions removed
// to prevent user-controlled templates from leaking server process secrets.
func safeFuncMap() template.FuncMap {
	fm := sprig.FuncMap()
	// Remove functions that can read the server process environment.
	// An attacker who controls the message string could exfiltrate SESSION_SECRET,
	// DATABASE_URL, or any other env var present in the server process.
	delete(fm, "env")
	delete(fm, "expandenv")

	return fm
}

// RenderTemplate renders a Go template string with the current context using Sprig functions.
func (n *Notifier) RenderTemplate(templateStr string) (string, error) {
	n.mu.RLock()
	ctx := n.context
	n.mu.RUnlock()

	tmpl, err := template.New("notify").Funcs(safeFuncMap()).Parse(templateStr)
	if err != nil {
		return "", fmt.Errorf("could not parse template: %w", err)
	}

	var buf bytes.Buffer

	err = tmpl.Execute(&buf, ctx)
	if err != nil {
		return "", fmt.Errorf("could not execute template: %w", err)
	}

	return buf.String(), nil
}

// Send sends a notification using the named configuration.
func (n *Notifier) Send(ctx context.Context, name string, message string) error {
	if n.Disabled {
		return errors.New("notifications feature is not enabled")
	}

	n.mu.RLock()
	config, exists := n.configs[name]
	n.mu.RUnlock()

	if !exists {
		return fmt.Errorf("notification config %q not found", name)
	}

	// Resolve secret references in config fields.
	var err error

	config, err = n.resolveConfigSecrets(ctx, config)
	if err != nil {
		return fmt.Errorf("could not resolve secrets for notification %q: %w", name, err)
	}

	// Render the message template
	renderedMessage, err := n.RenderTemplate(message)
	if err != nil {
		return fmt.Errorf("could not render message template: %w", err)
	}

	n.logger.Debug("notification.sending",
		"name", name,
		"type", config.Type,
		"message_length", len(renderedMessage),
	)

	// Create and configure the notify service
	adapter, ok := n.adapters[config.Type]
	if !ok {
		return fmt.Errorf("unsupported notification type: %s", config.Type)
	}

	notifier := notify.New()

	err = adapter.Configure(notifier, config)
	if err != nil {
		return fmt.Errorf("could not configure %s service: %w", config.Type, err)
	}

	// Send the notification
	err = notifier.Send(ctx, "Pipeline Notification", renderedMessage)
	if err != nil {
		n.logger.Error("notification.send.sync.failed",
			"name", name,
			"type", config.Type,
			"error", err,
		)

		return fmt.Errorf("could not send notification: %w", err)
	}

	n.logger.Info("notification.send.success",
		"name", name,
		"type", config.Type,
	)

	return nil
}

// NotifyRuntime wraps Notifier for use in Goja VM.
type NotifyRuntime struct {
	notifier *Notifier
	jsVM     *goja.Runtime
	promises *sync.WaitGroup
	tasks    chan func() error
	ctx      context.Context //nolint: containedctx
}

// NewNotifyRuntime creates a NotifyRuntime for Goja integration.
func NewNotifyRuntime(
	ctx context.Context,
	jsVM *goja.Runtime,
	notifier *Notifier,
	promises *sync.WaitGroup,
	tasks chan func() error,
) *NotifyRuntime {
	return &NotifyRuntime{
		ctx:      ctx,
		jsVM:     jsVM,
		notifier: notifier,
		promises: promises,
		tasks:    tasks,
	}
}

// ConfigureInput is the input for notify.configure().
type ConfigureInput struct {
	Backends map[string]NotifyConfig `json:"backends"`
	Context  *NotifyContext          `json:"context"`
}

// Configure sets both notification backends and context in a single call.
func (nr *NotifyRuntime) Configure(input ConfigureInput) {
	if input.Backends != nil {
		nr.notifier.SetConfigs(input.Backends)
	}

	if input.Context != nil {
		nr.notifier.SetContext(*input.Context)
	}
}

// SetConfigs sets notification configurations from JavaScript.
func (nr *NotifyRuntime) SetConfigs(configs map[string]NotifyConfig) {
	nr.notifier.SetConfigs(configs)
}

// SetContext sets the pipeline context from JavaScript.
func (nr *NotifyRuntime) SetContext(ctx NotifyContext) {
	nr.notifier.SetContext(ctx)
}

// UpdateContext applies a partial update to the current notification context.
// Only non-zero fields in the input are applied.
func (nr *NotifyRuntime) UpdateContext(partial NotifyContext) {
	nr.notifier.UpdateContext(func(c *NotifyContext) {
		if partial.Status != "" {
			c.Status = partial.Status
		}

		if partial.JobName != "" {
			c.JobName = partial.JobName
		}

		if partial.PipelineName != "" {
			c.PipelineName = partial.PipelineName
		}

		if partial.BuildID != "" {
			c.BuildID = partial.BuildID
		}

		if partial.EndTime != "" {
			c.EndTime = partial.EndTime
		}

		if partial.Duration != "" {
			c.Duration = partial.Duration
		}

		if partial.Environment != nil {
			c.Environment = partial.Environment
		}

		if partial.TaskResults != nil {
			c.TaskResults = partial.TaskResults
		}
	})
}

// UpdateStatus updates the status in the current context.
func (nr *NotifyRuntime) UpdateStatus(status string) {
	nr.notifier.UpdateContext(func(c *NotifyContext) {
		c.Status = status
	})
}

// UpdateJobName updates the job name in the current context.
func (nr *NotifyRuntime) UpdateJobName(jobName string) {
	nr.notifier.UpdateContext(func(c *NotifyContext) {
		c.JobName = jobName
	})
}

// Send sends a notification synchronously (returns a Promise).
func (nr *NotifyRuntime) Send(input NotifyInput) *goja.Promise {
	promise, resolve, reject := nr.jsVM.NewPromise()

	if input.Async {
		// Fire-and-forget mode
		go func() {
			err := nr.notifier.Send(nr.ctx, input.Name, input.Message)
			if err != nil {
				nr.notifier.logger.Error("notification.send.async.failed",
					"name", input.Name,
					"error", err,
				)
			}
		}()

		// Immediately resolve for async
		nr.promises.Add(1)
		nr.tasks <- func() error {
			defer nr.promises.Done()

			return resolve(NotifyResult{Success: true})
		}
	} else {
		// Synchronous mode with promise
		nr.promises.Add(1)

		go func() {
			err := nr.notifier.Send(nr.ctx, input.Name, input.Message)

			nr.tasks <- func() error {
				defer nr.promises.Done()

				if err != nil {
					result := NotifyResult{
						Success: false,
						Error:   err.Error(),
					}
					// Return the error result, let JS handle on_failure

					return reject(nr.jsVM.ToValue(result))
				}

				return resolve(NotifyResult{Success: true})
			}
		}()
	}

	return promise
}

// SendMultiple sends to multiple notification configs.
func (nr *NotifyRuntime) SendMultiple(input SendMultipleInput) *goja.Promise {
	names := input.Names
	message := input.Message
	async := input.Async
	promise, resolve, reject := nr.jsVM.NewPromise()

	nr.promises.Add(1)

	go func() {
		var errs []error

		for _, name := range names {
			err := nr.notifier.Send(nr.ctx, name, message)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
			}
		}

		nr.tasks <- func() error {
			defer nr.promises.Done()

			if len(errs) > 0 && !async {
				result := NotifyResult{
					Success: false,
					Error:   fmt.Sprintf("%d notification(s) failed", len(errs)),
				}

				return reject(nr.jsVM.ToValue(result))
			}

			return resolve(NotifyResult{Success: true})
		}
	}()

	return promise
}
