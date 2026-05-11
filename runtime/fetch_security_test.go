package runtime_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jtarchie/pocketci/runtime"
	. "github.com/onsi/gomega"
)

// TestFetchJSON_DoesNotEvalAttackerPayload codifies PCI-SEC-JS-001: an
// attacker-controlled response body whose contents are syntactically a JS
// expression (but not valid JSON) MUST cause `resp.json()` to throw a parse
// error, not execute the expression in the pipeline VM.
//
// Historically `FetchResponse.Json` used `vm.RunString("(" + body + ")")`,
// which evaluated arbitrary JS as the pipeline. The current implementation
// uses encoding/json so that JS expressions like `1, (function(){...})()`,
// IIFEs, comments, single-quoted strings, and template literals all fail to
// parse instead of executing.
func TestFetchJSON_DoesNotEvalAttackerPayload(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
	}{
		{
			name: "iife_with_throw",
			body: `1, (function(){throw "RCE";})()`,
		},
		{
			name: "iife_returning_object",
			body: `(function(){return {ok:true};})()`,
		},
		{
			name: "comma_operator_with_side_effect",
			body: `1, globalThis.pwned = true`,
		},
		{
			name: "single_quoted_string",
			body: `'this is not json'`,
		},
		{
			name: "template_literal",
			body: "`hello`",
		},
		{
			name: "line_comment",
			body: "1 // pretending to be json",
		},
		{
			name: "block_comment",
			body: "1 /* still not json */",
		},
		{
			name: "trailing_paren_pair",
			body: "(0).valueOf()",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			body := tc.body
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, body)
			}))
			defer ts.Close()

			js := runtime.NewJS(slog.Default())
			err := js.ExecuteWithOptions(context.Background(), fmt.Sprintf(`
				const pipeline = async () => {
					const resp = await fetch(%q);
					let threw = false;
					try {
						resp.json();
					} catch (e) {
						threw = true;
					}
					assert.equal(threw, true);
				};
				export { pipeline };
			`, ts.URL), nil, nil, runtime.ExecuteOptions{FetchAllowPrivateIPs: true})
			assert.Expect(err).NotTo(HaveOccurred())
		})
	}
}

// TestFetchJSON_AcceptsValidJSON ensures the parser still handles every
// shape of valid JSON: object, array, string, number, bool, null, nested.
func TestFetchJSON_AcceptsValidJSON(t *testing.T) {
	t.Parallel()

	body := `{"obj":{"k":"v"},"arr":[1,2,3],"s":"hi","n":42,"b":true,"z":null}`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, body)
	}))
	defer ts.Close()

	assert := NewGomegaWithT(t)
	js := runtime.NewJS(slog.Default())
	err := js.ExecuteWithOptions(context.Background(), fmt.Sprintf(`
		const pipeline = async () => {
			const resp = await fetch(%q);
			const data = resp.json();
			assert.equal(data.obj.k, "v");
			assert.equal(data.arr[0], 1);
			assert.equal(data.arr[2], 3);
			assert.equal(data.s, "hi");
			assert.equal(data.n, 42);
			assert.equal(data.b, true);
			assert.equal(data.z, null);
		};
		export { pipeline };
	`, ts.URL), nil, nil, runtime.ExecuteOptions{FetchAllowPrivateIPs: true})
	assert.Expect(err).NotTo(HaveOccurred())
}
