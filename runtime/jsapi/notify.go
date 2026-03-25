package jsapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"text/template"

	"github.com/dop251/goja"
	sprig "github.com/go-task/slim-sprig/v3"
	"github.com/jtarchie/pocketci/runtime/support"
	"github.com/jtarchie/pocketci/secrets"
	"github.com/nikoksr/notify"
	nhttp "github.com/nikoksr/notify/service/http"
	"github.com/nikoksr/notify/service/msteams"
	"github.com/nikoksr/notify/service/slack"
)

// NotifyConfig represents the configuration for a notification backend.
type NotifyConfig struct {
	Type       string            `json:"type"`       // slack, teams, http
	Token      string            `json:"token"`      // For Slack
	Webhook    string            `json:"webhook"`    // For Teams
	URL        string            `json:"url"`        // For HTTP
	Channels   []string          `json:"channels"`   // For Slack
	Headers    map[string]string `json:"headers"`    // For HTTP
	Method     string            `json:"method"`     // For HTTP (defaults to POST)
	Recipients []string          `json:"recipients"` // Generic recipients
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
	context        NotifyContext
	logger         *slog.Logger
	mu             sync.RWMutex
	Disabled       bool
	secretsManager secrets.Manager
	pipelineID     string
}

// NewNotifier creates a new Notifier instance.
func NewNotifier(logger *slog.Logger) *Notifier {
	return &Notifier{
		configs: make(map[string]NotifyConfig),
		logger:  logger.WithGroup("notifier.send"),
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

// RenderTemplate renders a Go template string with the current context using Sprig functions.
func (n *Notifier) RenderTemplate(templateStr string) (string, error) {
	n.mu.RLock()
	ctx := n.context
	n.mu.RUnlock()

	tmpl, err := template.New("notify").Funcs(sprig.FuncMap()).Parse(templateStr)
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
	notifier := notify.New()

	var configErr error

	switch config.Type {
	case "slack":
		configErr = n.configureSlack(notifier, config)
	case "teams":
		configErr = n.configureTeams(notifier, config)
	case "http":
		configErr = n.configureHTTP(notifier, config)
	default:
		return fmt.Errorf("unsupported notification type: %s", config.Type)
	}

	if configErr != nil {
		return fmt.Errorf("could not configure %s service: %w", config.Type, configErr)
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

func (n *Notifier) configureSlack(notifier *notify.Notify, config NotifyConfig) error {
	if config.Token == "" {
		return errors.New("slack token is required")
	}

	slackService := slack.New(config.Token)

	for _, channel := range config.Channels {
		slackService.AddReceivers(channel)
	}

	for _, recipient := range config.Recipients {
		slackService.AddReceivers(recipient)
	}

	notifier.UseServices(slackService)

	return nil
}

func (n *Notifier) configureTeams(notifier *notify.Notify, config NotifyConfig) error {
	if config.Webhook == "" {
		return errors.New("teams webhook URL is required")
	}

	teamsService := msteams.New()
	teamsService.AddReceivers(config.Webhook)

	notifier.UseServices(teamsService)

	return nil
}

func (n *Notifier) configureHTTP(notifier *notify.Notify, config NotifyConfig) error {
	if config.URL == "" {
		return errors.New("HTTP URL is required")
	}

	method := config.Method
	if method == "" {
		method = http.MethodPost
	}

	httpService := nhttp.New()
	httpService.AddReceivers(&nhttp.Webhook{
		URL:         config.URL,
		Header:      n.headersToHTTPHeader(config.Headers),
		ContentType: "application/json",
		Method:      method,
		BuildPayload: func(subject, message string) (payload any) {
			return map[string]string{
				"subject": subject,
				"message": message,
			}
		},
	})

	notifier.UseServices(httpService)

	return nil
}

func (n *Notifier) headersToHTTPHeader(headers map[string]string) http.Header {
	h := make(http.Header)
	for k, v := range headers {
		h.Set(k, v)
	}

	return h
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

// SetConfigs sets notification configurations from JavaScript.
func (nr *NotifyRuntime) SetConfigs(configs map[string]NotifyConfig) {
	nr.notifier.SetConfigs(configs)
}

// SetContext sets the pipeline context from JavaScript.
func (nr *NotifyRuntime) SetContext(ctx NotifyContext) {
	nr.notifier.SetContext(ctx)
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
