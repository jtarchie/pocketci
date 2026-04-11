package runtime_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jtarchie/pocketci/runtime"
	. "github.com/onsi/gomega"
)

func newTestServer() *httptest.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/text", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, "hello world")
	})

	mux.HandleFunc("/json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"greeting":"hello","count":42}`)
	})

	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		defer func() { _ = r.Body.Close() }()

		resp := map[string]any{
			"method":  r.Method,
			"body":    string(body),
			"headers": map[string]string{},
		}

		for k, v := range r.Header {
			if len(v) > 0 {
				resp["headers"].(map[string]string)[k] = v[0]
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/status/", func(w http.ResponseWriter, r *http.Request) {
		code := 200
		parts := strings.Split(r.URL.Path, "/")

		if len(parts) >= 3 {
			_, _ = fmt.Sscanf(parts[2], "%d", &code)
		}

		w.WriteHeader(code)
		_, _ = fmt.Fprintf(w, "status: %d", code)
	})

	mux.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(3 * time.Second)
		_, _ = fmt.Fprint(w, "done")
	})

	mux.HandleFunc("/large", func(w http.ResponseWriter, _ *http.Request) {
		data := strings.Repeat("x", 2*1024*1024)
		_, _ = fmt.Fprint(w, data)
	})

	return httptest.NewServer(mux)
}

func TestFetchBasicGET(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)
	ts := newTestServer()
	defer ts.Close()

	js := runtime.NewJS(slog.Default())
	err := js.ExecuteWithOptions(context.Background(), fmt.Sprintf(`
		const pipeline = async () => {
			const resp = await fetch("%s/text");
			assert.equal(resp.status, 200);
			assert.equal(resp.ok, true);
			assert.equal(resp.statusText, "OK");
			const body = resp.text();
			assert.equal(body, "hello world");
		};
		export { pipeline };
	`, ts.URL), nil, nil, runtime.ExecuteOptions{FetchAllowPrivateIPs: true})
	assert.Expect(err).NotTo(HaveOccurred())
}

func TestFetchJSON(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)
	ts := newTestServer()
	defer ts.Close()

	js := runtime.NewJS(slog.Default())
	err := js.ExecuteWithOptions(context.Background(), fmt.Sprintf(`
		const pipeline = async () => {
			const resp = await fetch("%s/json");
			assert.equal(resp.status, 200);
			const data = resp.json();
			assert.equal(data.greeting, "hello");
			assert.equal(data.count, 42);
		};
		export { pipeline };
	`, ts.URL), nil, nil, runtime.ExecuteOptions{FetchAllowPrivateIPs: true})
	assert.Expect(err).NotTo(HaveOccurred())
}

func TestFetchPOSTWithBody(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)
	ts := newTestServer()
	defer ts.Close()

	js := runtime.NewJS(slog.Default())
	err := js.ExecuteWithOptions(context.Background(), fmt.Sprintf(`
		const pipeline = async () => {
			const resp = await fetch("%s/echo", {
				method: "POST",
				headers: { "Content-Type": "application/json", "X-Custom": "test-value" },
				body: JSON.stringify({ message: "hello" }),
			});
			assert.equal(resp.status, 200);
			const data = resp.json();
			assert.equal(data.method, "POST");
			assert.containsString(data.body, "hello");
			assert.equal(data.headers["X-Custom"], "test-value");
		};
		export { pipeline };
	`, ts.URL), nil, nil, runtime.ExecuteOptions{FetchAllowPrivateIPs: true})
	assert.Expect(err).NotTo(HaveOccurred())
}

func TestFetchResponseHeaders(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)
	ts := newTestServer()
	defer ts.Close()

	js := runtime.NewJS(slog.Default())
	err := js.ExecuteWithOptions(context.Background(), fmt.Sprintf(`
		const pipeline = async () => {
			const resp = await fetch("%s/json");
			assert.equal(resp.headers["content-type"], "application/json");
		};
		export { pipeline };
	`, ts.URL), nil, nil, runtime.ExecuteOptions{FetchAllowPrivateIPs: true})
	assert.Expect(err).NotTo(HaveOccurred())
}

func TestFetchNon200Status(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)
	ts := newTestServer()
	defer ts.Close()

	js := runtime.NewJS(slog.Default())
	err := js.ExecuteWithOptions(context.Background(), fmt.Sprintf(`
		const pipeline = async () => {
			const resp = await fetch("%s/status/404");
			assert.equal(resp.status, 404);
			assert.equal(resp.ok, false);
			const body = resp.text();
			assert.containsString(body, "404");
		};
		export { pipeline };
	`, ts.URL), nil, nil, runtime.ExecuteOptions{FetchAllowPrivateIPs: true})
	assert.Expect(err).NotTo(HaveOccurred())
}

func TestFetchServerError(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)
	ts := newTestServer()
	defer ts.Close()

	js := runtime.NewJS(slog.Default())
	err := js.ExecuteWithOptions(context.Background(), fmt.Sprintf(`
		const pipeline = async () => {
			const resp = await fetch("%s/status/500");
			assert.equal(resp.status, 500);
			assert.equal(resp.ok, false);
		};
		export { pipeline };
	`, ts.URL), nil, nil, runtime.ExecuteOptions{FetchAllowPrivateIPs: true})
	assert.Expect(err).NotTo(HaveOccurred())
}

func TestFetchTimeout(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)
	ts := newTestServer()
	defer ts.Close()

	js := runtime.NewJS(slog.Default())
	err := js.ExecuteWithOptions(context.Background(), fmt.Sprintf(`
		const pipeline = async () => {
			try {
				await fetch("%s/slow", { timeout: 100 });
				throw new Error("should have timed out");
			} catch (e) {
				assert.containsString(e.toString(), "Timeout");
			}
		};
		export { pipeline };
	`, ts.URL), nil, nil, runtime.ExecuteOptions{FetchAllowPrivateIPs: true})
	assert.Expect(err).NotTo(HaveOccurred())
}

func TestFetchMaxResponseSize(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)
	ts := newTestServer()
	defer ts.Close()

	js := runtime.NewJS(slog.Default())
	err := js.ExecuteWithOptions(context.Background(), fmt.Sprintf(`
		const pipeline = async () => {
			try {
				await fetch("%s/large");
				throw new Error("should have exceeded max size");
			} catch (e) {
				assert.containsString(e.toString(), "maximum size");
			}
		};
		export { pipeline };
	`, ts.URL), nil, nil, runtime.ExecuteOptions{
		FetchMaxResponseBytes: 1 * 1024 * 1024,
		FetchAllowPrivateIPs:  true,
	})
	assert.Expect(err).NotTo(HaveOccurred())
}

func TestFetchDisabled(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)
	ts := newTestServer()
	defer ts.Close()

	js := runtime.NewJS(slog.Default())
	err := js.ExecuteWithOptions(context.Background(), fmt.Sprintf(`
		const pipeline = async () => {
			try {
				await fetch("%s/text");
				throw new Error("should have been disabled");
			} catch (e) {
				assert.containsString(e.toString(), "not enabled");
			}
		};
		export { pipeline };
	`, ts.URL), nil, nil, runtime.ExecuteOptions{
		DisableFetch: true,
	})
	assert.Expect(err).NotTo(HaveOccurred())
}

func TestFetchInvalidURL(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	js := runtime.NewJS(slog.Default())
	err := js.Execute(context.Background(), `
		const pipeline = async () => {
			try {
				await fetch("http://localhost:1/nonexistent");
				throw new Error("should have failed");
			} catch (e) {
				assert.containsString(e.toString(), "Error");
			}
		};
		export { pipeline };
	`, nil, nil)
	assert.Expect(err).NotTo(HaveOccurred())
}

func TestFetchNoArguments(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	js := runtime.NewJS(slog.Default())
	err := js.Execute(context.Background(), `
		const pipeline = async () => {
			try {
				await fetch();
				throw new Error("should have failed");
			} catch (e) {
				assert.containsString(e.toString(), "URL");
			}
		};
		export { pipeline };
	`, nil, nil)
	assert.Expect(err).NotTo(HaveOccurred())
}

func TestFetchPUTMethod(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)
	ts := newTestServer()
	defer ts.Close()

	js := runtime.NewJS(slog.Default())
	err := js.ExecuteWithOptions(context.Background(), fmt.Sprintf(`
		const pipeline = async () => {
			const resp = await fetch("%s/echo", {
				method: "PUT",
				body: "updated content",
			});
			const data = resp.json();
			assert.equal(data.method, "PUT");
			assert.containsString(data.body, "updated content");
		};
		export { pipeline };
	`, ts.URL), nil, nil, runtime.ExecuteOptions{FetchAllowPrivateIPs: true})
	assert.Expect(err).NotTo(HaveOccurred())
}

func TestFetchDELETEMethod(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)
	ts := newTestServer()
	defer ts.Close()

	js := runtime.NewJS(slog.Default())
	err := js.ExecuteWithOptions(context.Background(), fmt.Sprintf(`
		const pipeline = async () => {
			const resp = await fetch("%s/echo", {
				method: "DELETE",
			});
			const data = resp.json();
			assert.equal(data.method, "DELETE");
		};
		export { pipeline };
	`, ts.URL), nil, nil, runtime.ExecuteOptions{FetchAllowPrivateIPs: true})
	assert.Expect(err).NotTo(HaveOccurred())
}

func TestFetchMultipleSequential(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)
	ts := newTestServer()
	defer ts.Close()

	js := runtime.NewJS(slog.Default())
	err := js.ExecuteWithOptions(context.Background(), fmt.Sprintf(`
		const pipeline = async () => {
			const r1 = await fetch("%s/text");
			assert.equal(r1.text(), "hello world");

			const r2 = await fetch("%s/json");
			const data = r2.json();
			assert.equal(data.greeting, "hello");

			const r3 = await fetch("%s/status/201");
			assert.equal(r3.status, 201);
			assert.equal(r3.ok, true);
		};
		export { pipeline };
	`, ts.URL, ts.URL, ts.URL), nil, nil, runtime.ExecuteOptions{FetchAllowPrivateIPs: true})
	assert.Expect(err).NotTo(HaveOccurred())
}
