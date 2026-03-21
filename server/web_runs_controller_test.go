package server_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	_ "github.com/jtarchie/pocketci/orchestra/native"
	"github.com/jtarchie/pocketci/server"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func TestCountTaskStatsCountsErrorAsFailureViaHTTP(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	buildFile, err := os.CreateTemp(t.TempDir(), "")
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = buildFile.Close() }()

	client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = client.Close() }()

	// Pipeline that writes tasks with various statuses including "error"
	pipelineContent := `
export const pipeline = async () => {
	const runID = typeof pipelineContext !== "undefined" && pipelineContext.runID ? pipelineContext.runID : String(Date.now());
	storage.set("/pipeline/" + runID + "/tasks/task-success", { status: "success" });
	storage.set("/pipeline/" + runID + "/tasks/task-failure", { status: "failure" });
	storage.set("/pipeline/" + runID + "/tasks/task-error", { status: "error" });
	storage.set("/pipeline/" + runID + "/tasks/task-pending", { status: "pending" });
	storage.set("/pipeline/" + runID + "/tasks/task-unknown", {});
};`

	pipeline, err := client.SavePipeline(context.Background(), "stats-pipeline", pipelineContent, "native", "")
	assert.Expect(err).NotTo(HaveOccurred())

	router := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 5})
	execService := router.ExecutionService()

	run, err := execService.TriggerPipeline(context.Background(), pipeline, nil)
	assert.Expect(err).NotTo(HaveOccurred())
	execService.Wait()

	// GET the run page and verify stats are rendered in the HTML
	req := httptest.NewRequest(http.MethodGet, "/runs/"+run.ID+"/tasks", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Expect(rec.Code).To(Equal(http.StatusOK))

	doc := mustHTMLDocument(t, rec)
	// The stats should show: 1 success, 2 failure (failure + error), 2 pending (pending + unknown)
	assert.Expect(strings.TrimSpace(doc.Find("#stat-success").Text())).To(Equal("1"))
	assert.Expect(strings.TrimSpace(doc.Find("#stat-failure").Text())).To(Equal("2"))
	assert.Expect(strings.TrimSpace(doc.Find("#stat-pending").Text())).To(Equal("2"))
}
