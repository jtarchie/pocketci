package server_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jtarchie/pocketci/server"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func TestHTMLEndpointsAreStrictlyValid(t *testing.T) {
	t.Parallel()

	t.Run("sqlite", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		buildFile, err := os.CreateTemp(t.TempDir(), "")
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = buildFile.Close() }()

		client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = client.Close() }()

		pipeline, err := client.SavePipeline(context.Background(), "html-validation-pipeline", "export const pipeline = async () => {};", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		run, err := client.SaveRun(context.Background(), pipeline.ID)
		assert.Expect(err).NotTo(HaveOccurred())

		err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/0-build", map[string]any{
			"status": "success",
			"logs":   []map[string]any{{"type": "stdout", "content": "ok"}},
		})
		assert.Expect(err).NotTo(HaveOccurred())

		router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
		assert.Expect(err).NotTo(HaveOccurred())

		fullDocumentEndpoints := []string{
			"/pipelines/",
			"/pipelines/" + pipeline.ID + "/",
			"/pipelines/" + pipeline.ID + "/source/",
			"/runs/" + run.ID + "/tasks",
			"/runs/" + run.ID + "/graph",
		}

		for _, endpoint := range fullDocumentEndpoints {
			req := httptest.NewRequest(http.MethodGet, endpoint, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusOK), endpoint)
			assert.Expect(rec.Header().Get("Content-Type")).To(ContainSubstring("text/html"), endpoint)
			mustBeValidHTMLDocumentStrict(t, rec)
		}

		fragmentEndpoints := []string{
			"/pipelines/" + pipeline.ID + "/runs-section/",
			"/pipelines/" + pipeline.ID + "/runs-search/?q=build",
			"/runs/" + run.ID + "/tasks-partial/",
			"/runs/" + run.ID + "/graph-data/",
			"/terminal/pipeline/" + run.ID + "/tasks/0-build",
		}

		for _, endpoint := range fragmentEndpoints {
			req := httptest.NewRequest(http.MethodGet, endpoint, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(BeNumerically(">=", 200), endpoint)
			assert.Expect(rec.Code).To(BeNumerically("<", 300), endpoint)
			mustBeValidHTMLFragmentStrict(t, rec)
		}
	})
}
