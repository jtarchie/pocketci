package client

import (
	"net/url"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
)

// Client is an HTTP client for the PocketCI API.
type Client struct {
	http      *resty.Client
	serverURL string
}

// Option configures a Client.
type Option func(*Client)

// WithAuthToken sets a bearer token for authentication.
func WithAuthToken(token string) Option {
	return func(c *Client) {
		c.http.SetAuthToken(token)
	}
}

// WithBasicAuth sets basic authentication credentials.
func WithBasicAuth(username, password string) Option {
	return func(c *Client) {
		c.http.SetBasicAuth(username, password)
	}
}

// WithTimeout sets the HTTP client timeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		c.http.SetTimeout(d)
	}
}

// New creates a new PocketCI API client. It normalizes the server URL,
// extracts embedded basic auth credentials, and applies the given options.
func New(serverURL string, opts ...Option) *Client {
	serverURL = strings.TrimSuffix(serverURL, "/")

	c := &Client{
		http:      resty.New(),
		serverURL: serverURL,
	}

	if parsed, err := url.Parse(serverURL); err == nil && parsed.User != nil {
		password, _ := parsed.User.Password()
		c.http.SetBasicAuth(parsed.User.Username(), password)
		parsed.User = nil
		c.serverURL = parsed.String()
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// ServerURL returns the normalized server URL.
func (c *Client) ServerURL() string {
	return c.serverURL
}
