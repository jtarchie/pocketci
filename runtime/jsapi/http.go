package jsapi

import (
	"sync"

	"github.com/dop251/goja"
)

// WebhookData represents the incoming HTTP request data from a webhook trigger.
type WebhookData struct {
	Provider  string            `json:"provider"`
	EventType string            `json:"eventType"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"`
	Query     map[string]string `json:"query"`
}

// HTTPResponse represents the HTTP response a pipeline can send back to the webhook caller.
type HTTPResponse struct {
	Status  int               `json:"status"`
	Body    string            `json:"body"`
	Headers map[string]string `json:"headers"`
}

// HTTPRuntime is the object exposed to JavaScript as the global `http` object.
// It provides access to the incoming webhook request and allows the pipeline
// to send an HTTP response back to the caller while continuing execution.
type HTTPRuntime struct {
	jsVM         *goja.Runtime
	request      *WebhookData
	responseChan chan *HTTPResponse
	responded    sync.Once
}

// NewHTTPRuntime creates a new HTTPRuntime.
// If webhookData is nil (non-webhook trigger), request() returns undefined and respond() is a no-op.
// responseChan should be a buffered channel of size 1; it may be nil for non-webhook triggers.
func NewHTTPRuntime(jsVM *goja.Runtime, webhookData *WebhookData, responseChan chan *HTTPResponse) *HTTPRuntime {
	return &HTTPRuntime{
		jsVM:         jsVM,
		request:      webhookData,
		responseChan: responseChan,
	}
}

// Request returns the incoming HTTP request data, or undefined if not triggered via webhook.
func (h *HTTPRuntime) Request() any {
	if h.request == nil {
		return goja.Undefined()
	}

	return h.request
}

// Respond sends an HTTP response back to the webhook caller.
// This is a one-shot operation — subsequent calls are silently ignored.
// If not triggered via webhook, this is a no-op.
func (h *HTTPRuntime) Respond(call goja.FunctionCall) goja.Value {
	if h.responseChan == nil {
		return goja.Undefined()
	}

	arg := call.Argument(0)
	if arg == nil || goja.IsUndefined(arg) || goja.IsNull(arg) {
		return goja.Undefined()
	}

	var resp HTTPResponse

	err := h.jsVM.ExportTo(arg, &resp)
	if err != nil {
		panic(h.jsVM.NewGoError(err))
	}

	if resp.Status == 0 {
		resp.Status = 200
	}

	h.responded.Do(func() {
		h.responseChan <- &resp
	})

	return goja.Undefined()
}
