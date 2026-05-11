package webhooks

import "net/http"

// NewEvent constructs a normalised Event from the incoming request. Headers
// and query parameters are flattened to the first value per key. Providers
// call this from their Parse() implementations after a successful signature
// check.
func NewEvent(provider, eventType string, r *http.Request, body []byte) *Event {
	headers := make(map[string]string, len(r.Header))
	for key, values := range r.Header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}

	query := make(map[string]string, len(r.URL.Query()))
	for key, values := range r.URL.Query() {
		if len(values) > 0 {
			query[key] = values[0]
		}
	}

	return &Event{
		Provider:  provider,
		EventType: eventType,
		Method:    r.Method,
		URL:       r.URL.String(),
		Headers:   headers,
		Body:      string(body),
		Query:     query,
	}
}
