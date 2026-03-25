package runtime_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jtarchie/pocketci/runtime"
	. "github.com/onsi/gomega"
)

func TestBrokenJS(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	js := runtime.NewJS(slog.Default())
	err := js.Execute(context.Background(), strings.TrimSpace(`
		export function pipeline() {
			const array = [];
			return array[1].asdf;
		};
	`), nil, nil)
	assert.Expect(err).To(HaveOccurred())
	assert.Expect(err.Error()).To(ContainSubstring("main.js:3"))
}

func TestAwaitPromise(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	js := runtime.NewJS(slog.Default())
	err := js.Execute(context.Background(), `
		async function pipeline() {
			await Promise.reject(400);
		};

		export { pipeline };
	`, nil, nil)
	assert.Expect(err).To(HaveOccurred())
}

func TestUseContext(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	js := runtime.NewJS(slog.Default())
	err := js.Execute(ctx, `
		async function pipeline() {
			for (; true; ) {}
		};

		export { pipeline };
	`, nil, nil)
	assert.Expect(err).To(HaveOccurred())
}

func TestYAMLAndAssert(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	js := runtime.NewJS(slog.Default())
	err := js.Execute(context.Background(), `
		async function pipeline() {
			const payload = yaml.parse("foo: bar");
			assert.equal(payload.foo, "bar");
		};

		export { pipeline };
	`, nil, nil)
	assert.Expect(err).NotTo(HaveOccurred())
}

func TestPipelineContextArgs(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	js := runtime.NewJS(slog.Default())
	err := js.ExecuteWithOptions(context.Background(), `
		async function pipeline() {
			assert.equal(pipelineContext.args.length, 2);
			assert.equal(pipelineContext.args[0], "hello");
			assert.equal(pipelineContext.args[1], "world");
		};

		export { pipeline };
	`, nil, nil, runtime.ExecuteOptions{
		Args: []string{"hello", "world"},
	})
	assert.Expect(err).NotTo(HaveOccurred())
}

func TestPipelineContextArgsDefaultsToEmpty(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	js := runtime.NewJS(slog.Default())
	err := js.Execute(context.Background(), `
		async function pipeline() {
			assert.equal(pipelineContext.args.length, 0);
		};

		export { pipeline };
	`, nil, nil)
	assert.Expect(err).NotTo(HaveOccurred())
}
