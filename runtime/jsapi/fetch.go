package jsapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/go-resty/resty/v2"
)

// privateIPRanges lists IP networks that should not be reachable from
// user-authored pipelines to prevent Server-Side Request Forgery (SSRF)
// against cloud metadata services, internal APIs, and RFC1918 networks.
var privateIPRanges = func() []*net.IPNet {
	blocks := []string{
		"127.0.0.0/8",    // IPv4 loopback
		"10.0.0.0/8",     // RFC 1918
		"172.16.0.0/12",  // RFC 1918
		"192.168.0.0/16", // RFC 1918
		"169.254.0.0/16", // link-local / cloud metadata (169.254.169.254)
		"100.64.0.0/10",  // shared address space (RFC 6598)
		"::1/128",        // IPv6 loopback
		"fc00::/7",       // IPv6 unique local
		"fe80::/10",      // IPv6 link-local
	}

	nets := make([]*net.IPNet, 0, len(blocks))
	for _, b := range blocks {
		_, network, err := net.ParseCIDR(b)
		if err == nil {
			nets = append(nets, network)
		}
	}

	return nets
}()

// isPrivateIP returns true if the given IP falls in any blocked private range.
func isPrivateIP(ip net.IP) bool {
	for _, network := range privateIPRanges {
		if network.Contains(ip) {
			return true
		}
	}

	return false
}

// ssrfSafeDialer returns a DialContext function that resolves the target
// hostname and rejects connections to private/internal IP addresses.
func ssrfSafeDialer() func(ctx context.Context, network, addr string) (net.Conn, error) {
	base := &net.Dialer{Timeout: 10 * time.Second}

	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid address %q: %w", addr, err)
		}

		addrs, err := net.DefaultResolver.LookupHost(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("could not resolve %q: %w", host, err)
		}

		for _, a := range addrs {
			ip := net.ParseIP(a)
			if ip != nil && isPrivateIP(ip) {
				return nil, fmt.Errorf("fetch blocked: %q resolves to private address %s", host, a)
			}
		}

		return base.DialContext(ctx, network, net.JoinHostPort(addrs[0], port))
	}
}

const (
	defaultFetchTimeout          = 30 * time.Second
	defaultFetchMaxResponseBytes = 10 * 1024 * 1024 // 10 MB
)

// FetchResponse represents the response returned to JavaScript from fetch().
type FetchResponse struct {
	Status     int               `json:"status"`
	StatusText string            `json:"statusText"`
	OK         bool              `json:"ok"`
	Headers    map[string]string `json:"headers"`
	bodyText   string
	jsVM       *goja.Runtime
}

// Text returns the response body as a string.
func (r *FetchResponse) Text() string {
	return r.bodyText
}

// Json parses the response body as JSON and returns the result.
// Named Json (not JSON) so goja's TagFieldNameMapper maps it to "json" in JS.
func (r *FetchResponse) Json() (any, error) {
	val, err := r.jsVM.RunString("(" + r.bodyText + ")")
	if err != nil {
		return nil, fmt.Errorf("could not parse JSON: %w", err)
	}

	return val.Export(), nil
}

// FetchRuntime provides the global fetch() function to the JavaScript runtime.
type FetchRuntime struct {
	ctx              context.Context //nolint:containedctx
	jsVM             *goja.Runtime
	promises         *sync.WaitGroup
	tasks            chan func() error
	Disabled         bool
	BlockPrivateIPs  bool // when true, requests to RFC1918/link-local addresses are rejected
	timeout          time.Duration
	maxResponseBytes int64
}

// NewFetchRuntime creates a new FetchRuntime.
func NewFetchRuntime(
	ctx context.Context,
	jsVM *goja.Runtime,
	promises *sync.WaitGroup,
	tasks chan func() error,
	timeout time.Duration,
	maxResponseBytes int64,
) *FetchRuntime {
	if timeout <= 0 {
		timeout = defaultFetchTimeout
	}

	if maxResponseBytes <= 0 {
		maxResponseBytes = defaultFetchMaxResponseBytes
	}

	return &FetchRuntime{
		ctx:              ctx,
		jsVM:             jsVM,
		promises:         promises,
		tasks:            tasks,
		timeout:          timeout,
		maxResponseBytes: maxResponseBytes,
		BlockPrivateIPs:  true, // default on: prevent SSRF to internal services
	}
}

// Fetch implements the global fetch(url, opts?) function.
// It returns a Promise that resolves to a FetchResponse.
func (f *FetchRuntime) Fetch(call goja.FunctionCall) goja.Value {
	promise, resolve, reject := f.jsVM.NewPromise()

	if f.Disabled {
		_ = reject(f.jsVM.NewGoError(errors.New("fetch feature is not enabled")))

		return f.jsVM.ToValue(promise)
	}

	// Parse arguments: fetch(url) or fetch(url, options)
	if len(call.Arguments) == 0 {
		_ = reject(f.jsVM.NewGoError(errors.New("fetch requires a URL argument")))

		return f.jsVM.ToValue(promise)
	}

	url := call.Arguments[0].String()

	method := http.MethodGet
	headers := make(map[string]string)
	body := ""
	perCallTimeout := f.timeout

	if len(call.Arguments) > 1 {
		optsVal := call.Arguments[1]
		if optsVal != nil && !goja.IsUndefined(optsVal) && !goja.IsNull(optsVal) {
			var parseErr error

			method, headers, body, perCallTimeout, parseErr = f.parseOpts(optsVal, method, headers, body, perCallTimeout)
			if parseErr != nil {
				_ = reject(f.jsVM.NewGoError(parseErr))

				return f.jsVM.ToValue(promise)
			}
		}
	}

	f.promises.Add(1)

	go func() {
		resp, err := f.doFetch(url, method, headers, body, perCallTimeout)

		f.tasks <- func() error {
			defer f.promises.Done()

			if err != nil {
				err = reject(f.jsVM.NewGoError(err))
				if err != nil {
					return fmt.Errorf("could not reject fetch: %w", err)
				}

				return nil
			}

			resp.jsVM = f.jsVM

			err = resolve(resp)
			if err != nil {
				return fmt.Errorf("could not resolve fetch: %w", err)
			}

			return nil
		}
	}()

	return f.jsVM.ToValue(promise)
}

func (f *FetchRuntime) doFetch(url, method string, headers map[string]string, body string, timeout time.Duration) (*FetchResponse, error) {
	client := resty.New().
		SetTimeout(timeout)

	if f.BlockPrivateIPs {
		transport := &http.Transport{
			DialContext: ssrfSafeDialer(),
		}
		client.SetTransport(transport)
	}

	r := client.R().
		SetContext(f.ctx).
		SetDoNotParseResponse(true)

	for k, v := range headers {
		r.SetHeader(k, v)
	}

	if body != "" {
		r.SetBody(body)
	}

	resp, err := r.Execute(method, url)
	if err != nil {
		return nil, fmt.Errorf("fetch failed: %w", err)
	}
	defer func() { _ = resp.RawBody().Close() }()

	// Read body with size limit
	limited := io.LimitReader(resp.RawBody(), f.maxResponseBytes+1)

	bodyBytes, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("could not read response body: %w", err)
	}

	if int64(len(bodyBytes)) > f.maxResponseBytes {
		return nil, fmt.Errorf("response body exceeds maximum size of %d bytes", f.maxResponseBytes)
	}

	// Convert response headers (first value only)
	respHeaders := make(map[string]string)
	for k, v := range resp.Header() {
		if len(v) > 0 {
			respHeaders[strings.ToLower(k)] = v[0]
		}
	}

	statusCode := resp.StatusCode()

	return &FetchResponse{
		Status:     statusCode,
		StatusText: http.StatusText(statusCode),
		OK:         statusCode >= 200 && statusCode < 300,
		Headers:    respHeaders,
		bodyText:   string(bodyBytes),
	}, nil
}

// parseOpts extracts method, headers, body, and timeout from a JS options object.
func (f *FetchRuntime) parseOpts(
	optsVal goja.Value,
	method string,
	headers map[string]string,
	body string,
	timeout time.Duration,
) (string, map[string]string, string, time.Duration, error) {
	optsObj := optsVal.ToObject(f.jsVM)

	if m := optsObj.Get("method"); m != nil && !goja.IsUndefined(m) {
		method = strings.ToUpper(m.String())
	}

	if h := optsObj.Get("headers"); h != nil && !goja.IsUndefined(h) {
		err := f.jsVM.ExportTo(h, &headers)
		if err != nil {
			return "", nil, "", 0, fmt.Errorf("invalid headers: %w", err)
		}
	}

	if b := optsObj.Get("body"); b != nil && !goja.IsUndefined(b) {
		body = b.String()
	}

	if t := optsObj.Get("timeout"); t != nil && !goja.IsUndefined(t) {
		ms := t.ToInteger()
		if ms > 0 {
			timeout = time.Duration(ms) * time.Millisecond
		}
	}

	return method, headers, body, timeout, nil
}
