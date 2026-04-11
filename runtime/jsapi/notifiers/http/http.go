package http

import (
	"errors"
	nethttp "net/http"

	"github.com/jtarchie/pocketci/runtime/jsapi"
	"github.com/nikoksr/notify"
	nhttp "github.com/nikoksr/notify/service/http"
)

// Type is the identifier used in NotifyConfig.Type.
const Type = "http"

type adapter struct{}

// New returns the HTTP webhook notification adapter.
func New() jsapi.Adapter {
	return &adapter{}
}

func (a *adapter) Name() string { return Type }

func (a *adapter) Configure(n *notify.Notify, config jsapi.NotifyConfig) error {
	if config.URL == "" {
		return errors.New("HTTP URL is required")
	}

	method := config.Method
	if method == "" {
		method = nethttp.MethodPost
	}

	svc := nhttp.New()
	svc.AddReceivers(&nhttp.Webhook{
		URL:         config.URL,
		Header:      headersToHTTPHeader(config.Headers),
		ContentType: "application/json",
		Method:      method,
		BuildPayload: func(subject, message string) (payload any) {
			return map[string]string{
				"subject": subject,
				"message": message,
			}
		},
	})

	n.UseServices(svc)

	return nil
}

func headersToHTTPHeader(headers map[string]string) nethttp.Header {
	h := make(nethttp.Header)
	for k, v := range headers {
		h.Set(k, v)
	}

	return h
}
