package agent_test

import (
	"context"
	"log/slog"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/jtarchie/pocketci/runtime/agent"
	"github.com/jtarchie/pocketci/storage"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
)

func newMemoryTestStorage(t *testing.T) storage.Driver {
	t.Helper()

	assert := NewGomegaWithT(t)

	st, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test", slog.Default())
	assert.Expect(err).NotTo(HaveOccurred())

	t.Cleanup(func() { _ = st.Close() })

	return st
}

func TestBuildMemoryTools_DisabledByDefault(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	tools, err := agent.BuildMemoryToolsForTest(context.Background(), agent.AgentConfig{
		Name:       "reviewer",
		PipelineID: "pid",
		Storage:    newMemoryTestStorage(t),
	})
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(tools).To(BeEmpty())
}

func TestBuildMemoryTools_EnabledRequiresPipelineID(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	tools, err := agent.BuildMemoryToolsForTest(context.Background(), agent.AgentConfig{
		Name:    "reviewer",
		Memory:  &agent.AgentMemoryConfig{Enabled: true},
		Storage: newMemoryTestStorage(t),
	})
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(tools).To(BeEmpty(), "no tools without PipelineID")
}

func TestBuildMemoryTools_EnabledRequiresName(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	tools, err := agent.BuildMemoryToolsForTest(context.Background(), agent.AgentConfig{
		PipelineID: "pid",
		Memory:     &agent.AgentMemoryConfig{Enabled: true},
		Storage:    newMemoryTestStorage(t),
	})
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(tools).To(BeEmpty(), "no tools without Name")
}

func TestBuildMemoryTools_EnabledRequiresStorage(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	tools, err := agent.BuildMemoryToolsForTest(context.Background(), agent.AgentConfig{
		Name:       "reviewer",
		PipelineID: "pid",
		Memory:     &agent.AgentMemoryConfig{Enabled: true},
	})
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(tools).To(BeEmpty(), "no tools without Storage")
}

func TestBuildMemoryTools_Enabled(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	tools, err := agent.BuildMemoryToolsForTest(context.Background(), agent.AgentConfig{
		Name:       "reviewer",
		PipelineID: "pid",
		Memory:     &agent.AgentMemoryConfig{Enabled: true},
		Storage:    newMemoryTestStorage(t),
	})
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(tools).To(HaveLen(2))

	names := []string{tools[0].Name(), tools[1].Name()}
	assert.Expect(names).To(ConsistOf("recall_memory", "save_memory"))
}
