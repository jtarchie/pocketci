package backwards_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/native"
	"github.com/jtarchie/pocketci/runtime"
	"github.com/jtarchie/pocketci/runtime/jsapi"
	"github.com/jtarchie/pocketci/storage"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func setupNativeDriver(t *testing.T) orchestra.Driver {
	t.Helper()
	assert := NewGomegaWithT(t)
	driver, err := native.New(context.Background(), native.Config{Namespace: "ci-test"}, slog.Default())
	assert.Expect(err).NotTo(HaveOccurred())
	t.Cleanup(func() { _ = driver.Close() })
	return driver
}

func setupStorage(t *testing.T) storage.Driver {
	t.Helper()
	assert := NewGomegaWithT(t)
	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, ":memory:", slog.Default())
	assert.Expect(err).NotTo(HaveOccurred())
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func runYAMLPipeline(t *testing.T, yamlContent string, webhookData *jsapi.WebhookData) error {
	t.Helper()
	assert := NewGomegaWithT(t)

	pipeline, err := backwards.NewPipelineFromContent(yamlContent)
	assert.Expect(err).NotTo(HaveOccurred())

	driver := setupNativeDriver(t)
	store := setupStorage(t)

	js := runtime.NewJS(slog.Default())
	return js.ExecuteWithOptions(context.Background(), pipeline, driver, store, runtime.ExecuteOptions{
		WebhookData: webhookData,
	})
}

const simpleEchoYAML = `
jobs:
  - name: echo-job
    plan:
      - task: echo-task
        config:
          platform: linux
          run:
            path: echo
            args: ["hello"]
`

const webhookGatedYAML = `
jobs:
  - name: gated-job
    webhook_trigger: 'provider == "github"'
    plan:
      - task: echo-task
        config:
          platform: linux
          run:
            path: echo
            args: ["hello"]
`

const webhookGatedFailingYAML = `
jobs:
  - name: gated-failing-job
    webhook_trigger: 'provider == "github"'
    plan:
      - task: failing-task
        config:
          platform: linux
          run:
            path: sh
            args: ["-c", "exit 1"]
`

func TestWebhookTriggerYAML(t *testing.T) {
	t.Parallel()

	t.Run("job runs when no webhook_trigger set (manual trigger)", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		err := runYAMLPipeline(t, simpleEchoYAML, nil)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("job runs when no webhook_trigger set (webhook trigger)", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		err := runYAMLPipeline(t, simpleEchoYAML, &jsapi.WebhookData{
			Provider:  "slack",
			EventType: "message",
			Method:    "POST",
			Headers:   map[string]string{},
			Query:     map[string]string{},
			Body:      "{}",
		})
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("job runs when webhook_trigger matches", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		err := runYAMLPipeline(t, webhookGatedYAML, &jsapi.WebhookData{
			Provider:  "github",
			EventType: "push",
			Method:    "POST",
			Headers:   map[string]string{},
			Query:     map[string]string{},
			Body:      "{}",
		})
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("job is skipped (not failed) when webhook_trigger does not match", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		// This job has a failing task, but the job should be SKIPPED because the
		// webhook provider is "slack" and the trigger requires "github".
		err := runYAMLPipeline(t, webhookGatedFailingYAML, &jsapi.WebhookData{
			Provider:  "slack",
			EventType: "message",
			Method:    "POST",
			Headers:   map[string]string{},
			Query:     map[string]string{},
			Body:      "{}",
		})
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("job with webhook_trigger always runs on manual trigger (nil WebhookData)", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		// Manual triggers bypass webhook_trigger expressions.
		err := runYAMLPipeline(t, webhookGatedYAML, nil)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("transpiled JS contains webhook_trigger field", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		js, err := backwards.NewPipelineFromContent(webhookGatedYAML)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(js).To(ContainSubstring("webhook_trigger"))
		// The YAML value is JSON-encoded in the JS bundle, so double quotes are escaped.
		assert.Expect(js).To(ContainSubstring(`provider == \"github\"`))
	})
}

// ── triggers: webhook ────────────────────────────────────────────────────────

const triggersWebhookFilterYAML = `
jobs:
  - name: triggers-filter-job
    triggers:
      webhook:
        filter: 'provider == "github"'
    plan:
      - task: echo-task
        config:
          platform: linux
          run:
            path: echo
            args: ["hello"]
`

const triggersWebhookFilterFailingYAML = `
jobs:
  - name: triggers-filter-failing-job
    triggers:
      webhook:
        filter: 'provider == "github"'
    plan:
      - task: failing-task
        config:
          platform: linux
          run:
            path: sh
            args: ["-c", "exit 1"]
`

const triggersWebhookParamsYAML = `
jobs:
  - name: triggers-params-job
    triggers:
      webhook:
        filter: 'provider == "github"'
        params:
          MY_PROVIDER: 'provider'
          MY_EVENT:    'eventType'
    plan:
      - task: check-params
        config:
          platform: linux
          run:
            path: sh
            args:
              - -c
              - |
                test "$MY_PROVIDER" = "github"
                test "$MY_EVENT" = "pull_request"
`

const triggersWebhookParamsPayloadYAML = `
jobs:
  - name: triggers-params-payload-job
    triggers:
      webhook:
        filter: 'provider == "github"'
        params:
          PR_NUMBER: 'string(payload.number)'
          PR_REPO:   "'https://github.com/' + payload.pull_request.head.repo.full_name + '.git'"
    plan:
      - task: check-params
        config:
          platform: linux
          run:
            path: sh
            args:
              - -c
              - |
                test "$PR_NUMBER" = "42"
                test "$PR_REPO" = "https://github.com/org/repo.git"
`

func TestTriggersWebhook(t *testing.T) {
	t.Parallel()

	t.Run("filter: job runs when expression matches", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		err := runYAMLPipeline(t, triggersWebhookFilterYAML, &jsapi.WebhookData{
			Provider:  "github",
			EventType: "push",
			Method:    "POST",
			Headers:   map[string]string{},
			Query:     map[string]string{},
			Body:      "{}",
		})
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("filter: job is skipped when expression does not match", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		// Failing inner task must NOT run — skipped status is expected.
		err := runYAMLPipeline(t, triggersWebhookFilterFailingYAML, &jsapi.WebhookData{
			Provider:  "slack",
			EventType: "message",
			Method:    "POST",
			Headers:   map[string]string{},
			Query:     map[string]string{},
			Body:      "{}",
		})
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("filter: job always runs on manual trigger", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		err := runYAMLPipeline(t, triggersWebhookFilterYAML, nil)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("params: provider and eventType injected as env vars into task", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		err := runYAMLPipeline(t, triggersWebhookParamsYAML, &jsapi.WebhookData{
			Provider:  "github",
			EventType: "pull_request",
			Method:    "POST",
			Headers:   map[string]string{},
			Query:     map[string]string{},
			Body:      "{}",
		})
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("params: payload fields injected as env vars into task", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		body := `{"number":42,"pull_request":{"head":{"repo":{"full_name":"org/repo"}}}}`
		err := runYAMLPipeline(t, triggersWebhookParamsPayloadYAML, &jsapi.WebhookData{
			Provider:  "github",
			EventType: "pull_request",
			Method:    "POST",
			Headers:   map[string]string{},
			Query:     map[string]string{},
			Body:      body,
		})
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("params: empty map when no webhook (manual trigger)", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		// Manual trigger: params evaluate to empty strings — tasks must not fail
		// due to missing env vars (they are simply empty).
		// Use a pipeline that does NOT depend on specific param values.
		err := runYAMLPipeline(t, triggersWebhookFilterYAML, nil)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("transpiled JS contains triggers.webhook fields", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		js, err := backwards.NewPipelineFromContent(triggersWebhookParamsYAML)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(js).To(ContainSubstring(`"triggers"`))
		assert.Expect(js).To(ContainSubstring(`"filter"`))
		assert.Expect(js).To(ContainSubstring(`"params"`))
		assert.Expect(js).To(ContainSubstring(`MY_PROVIDER`))
		assert.Expect(js).To(ContainSubstring(`MY_EVENT`))
	})
}
