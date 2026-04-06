package runtime_test

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/jtarchie/pocketci/runtime"
	"github.com/jtarchie/pocketci/runtime/jsapi"
	. "github.com/onsi/gomega"
)

func runWithWebhook(t *testing.T, src string, data *jsapi.WebhookData) error {
	t.Helper()
	js := runtime.NewJS(slog.Default())

	err := js.ExecuteWithOptions(context.Background(), src, nil, nil, runtime.ExecuteOptions{
		WebhookData: data,
	})
	if err != nil {
		return fmt.Errorf("execute: %w", err)
	}

	return nil
}

func TestWebhookTriggerGlobal(t *testing.T) {
	t.Parallel()

	t.Run("returns true for manual trigger (nil WebhookData)", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		err := runWithWebhook(t, `
			async function pipeline() {
				assert.equal(webhookTrigger('provider == "github"'), true);
			}
			export { pipeline };
		`, nil)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("true when expression matches webhook", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		err := runWithWebhook(t, `
			async function pipeline() {
				assert.equal(webhookTrigger('provider == "github" && eventType == "push"'), true);
			}
			export { pipeline };
		`, &jsapi.WebhookData{
			Provider:  "github",
			EventType: "push",
			Method:    "POST",
			Headers:   map[string]string{},
			Query:     map[string]string{},
			Body:      "{}",
		})
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("false when expression does not match webhook", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		err := runWithWebhook(t, `
			async function pipeline() {
				assert.equal(webhookTrigger('eventType == "pull_request"'), false);
			}
			export { pipeline };
		`, &jsapi.WebhookData{
			Provider:  "github",
			EventType: "push",
			Method:    "POST",
			Headers:   map[string]string{},
			Query:     map[string]string{},
			Body:      "{}",
		})
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("can filter on nested JSON payload field", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		err := runWithWebhook(t, `
			async function pipeline() {
				assert.equal(webhookTrigger('payload["ref"] == "refs/heads/main"'), true);
			}
			export { pipeline };
		`, &jsapi.WebhookData{
			Provider:  "github",
			EventType: "push",
			Method:    "POST",
			Headers:   map[string]string{},
			Query:     map[string]string{},
			Body:      `{"ref":"refs/heads/main"}`,
		})
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("false (not panic) on invalid expression", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		err := runWithWebhook(t, `
			async function pipeline() {
				assert.equal(webhookTrigger('not valid expr !!!'), false);
			}
			export { pipeline };
		`, &jsapi.WebhookData{
			Provider:  "github",
			EventType: "push",
			Method:    "POST",
			Headers:   map[string]string{},
			Query:     map[string]string{},
			Body:      "{}",
		})
		assert.Expect(err).NotTo(HaveOccurred())
	})
}

func TestWebhookParamsGlobal(t *testing.T) {
	t.Parallel()

	t.Run("returns empty map for manual trigger (nil WebhookData)", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		err := runWithWebhook(t, `
			async function pipeline() {
				const params = webhookParams({ PR_NUMBER: 'string(payload.number)' });
				assert.equal(Object.keys(params).length, 0);
			}
			export { pipeline };
		`, nil)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("resolves provider expression", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		err := runWithWebhook(t, `
			async function pipeline() {
				const params = webhookParams({ MY_PROVIDER: 'provider' });
				assert.equal(params['MY_PROVIDER'], 'github');
			}
			export { pipeline };
		`, &jsapi.WebhookData{
			Provider:  "github",
			EventType: "pull_request",
			Method:    "POST",
			Headers:   map[string]string{},
			Query:     map[string]string{},
			Body:      "{}",
		})
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("resolves multiple expressions including payload field", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		err := runWithWebhook(t, `
			async function pipeline() {
				const params = webhookParams({
					PR_NUMBER: 'string(payload.number)',
					PR_REPO:   "'https://github.com/' + payload.pull_request.head.repo.full_name + '.git'",
				});
				assert.equal(params['PR_NUMBER'], '42');
				assert.equal(params['PR_REPO'], 'https://github.com/org/repo.git');
			}
			export { pipeline };
		`, &jsapi.WebhookData{
			Provider:  "github",
			EventType: "pull_request",
			Method:    "POST",
			Headers:   map[string]string{},
			Query:     map[string]string{},
			Body:      `{"number":42,"pull_request":{"head":{"repo":{"full_name":"org/repo"}}}}`,
		})
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("skips key and continues on invalid expression (no panic)", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		err := runWithWebhook(t, `
			async function pipeline() {
				const params = webhookParams({
					GOOD: 'provider',
					BAD:  'not valid expr !!!',
				});
				assert.equal(params['GOOD'], 'github');
				assert.equal(params['BAD'], undefined);
			}
			export { pipeline };
		`, &jsapi.WebhookData{
			Provider:  "github",
			EventType: "push",
			Method:    "POST",
			Headers:   map[string]string{},
			Query:     map[string]string{},
			Body:      "{}",
		})
		assert.Expect(err).NotTo(HaveOccurred())
	})
}
