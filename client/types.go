package client

import "encoding/json"

// SetPipelineRequest is the JSON body for PUT /api/pipelines/:name.
type SetPipelineRequest struct {
	Content        string            `json:"content"`
	ContentType    string            `json:"content_type"`
	Driver         string            `json:"driver"`
	DriverConfig   json.RawMessage   `json:"driver_config,omitempty"`
	WebhookSecret  string            `json:"webhook_secret"`
	Secrets        map[string]string `json:"secrets,omitempty"`
	ResumeEnabled  *bool             `json:"resume_enabled,omitempty"`
	RBACExpression *string           `json:"rbac_expression,omitempty"`
}

// TriggerRequest is the JSON body for POST /api/pipelines/:id/trigger.
type TriggerRequest struct {
	Mode    string             `json:"mode,omitempty"`
	Args    []string           `json:"args,omitempty"`
	Webhook *WebhookSimulation `json:"webhook,omitempty"`
}

// WebhookSimulation describes a simulated webhook for trigger requests.
type WebhookSimulation struct {
	Method  string            `json:"method"`
	Body    string            `json:"body,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// TriggerResult is the response from POST /api/pipelines/:id/trigger.
type TriggerResult struct {
	RunID   string `json:"run_id"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// SeedPassedResult is the response from POST /api/pipelines/:id/jobs/:name/seed-passed.
type SeedPassedResult struct {
	Job     string `json:"job"`
	RunID   string `json:"run_id"`
	Message string `json:"message"`
}

// DeviceFlowResult is the response from POST /auth/cli/begin.
type DeviceFlowResult struct {
	Code     string `json:"code"`
	LoginURL string `json:"login_url"`
}
