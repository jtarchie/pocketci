package client_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	"github.com/jtarchie/pocketci/client"
	"github.com/jtarchie/pocketci/storage"
)

func TestListPipelines(t *testing.T) {
	assert := NewGomegaWithT(t)

	result := storage.PaginationResult[storage.Pipeline]{
		Items: []storage.Pipeline{
			{ID: "p1", Name: "my-pipeline", Driver: "docker"},
		},
		Page:       1,
		PerPage:    20,
		TotalItems: 1,
		TotalPages: 1,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Expect(r.URL.Path).To(Equal("/api/pipelines"))
		assert.Expect(r.Method).To(Equal(http.MethodGet))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer srv.Close()

	c := client.New(srv.URL)
	got, err := c.ListPipelines()
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(got.Items).To(HaveLen(1))
	assert.Expect(got.Items[0].Name).To(Equal("my-pipeline"))
}

func TestFindPipelineByNameOrID(t *testing.T) {
	result := storage.PaginationResult[storage.Pipeline]{
		Items: []storage.Pipeline{
			{ID: "p1", Name: "first"},
			{ID: "p2", Name: "second"},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer srv.Close()

	c := client.New(srv.URL)

	t.Run("by name", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		p, err := c.FindPipelineByNameOrID("second")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(p.ID).To(Equal("p2"))
	})

	t.Run("by ID", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		p, err := c.FindPipelineByNameOrID("p1")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(p.Name).To(Equal("first"))
	})

	t.Run("not found", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		_, err := c.FindPipelineByNameOrID("nope")
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring("no pipeline found"))
	})
}

func TestSetPipeline(t *testing.T) {
	assert := NewGomegaWithT(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Expect(r.URL.Path).To(Equal("/api/pipelines/my-pipeline"))
		assert.Expect(r.Method).To(Equal(http.MethodPut))

		var req client.SetPipelineRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		assert.Expect(req.Content).To(Equal("console.log('hi')"))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(storage.Pipeline{
			ID:   "p1",
			Name: "my-pipeline",
		})
	}))
	defer srv.Close()

	c := client.New(srv.URL)
	p, err := c.SetPipeline("my-pipeline", client.SetPipelineRequest{
		Content:     "console.log('hi')",
		ContentType: "js",
		Driver:      "docker",
	})
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(p.ID).To(Equal("p1"))
}

func TestDeletePipeline(t *testing.T) {
	assert := NewGomegaWithT(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Expect(r.URL.Path).To(Equal("/api/pipelines/p1"))
		assert.Expect(r.Method).To(Equal(http.MethodDelete))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := client.New(srv.URL)
	err := c.DeletePipeline("p1")
	assert.Expect(err).NotTo(HaveOccurred())
}

func TestTriggerPipeline(t *testing.T) {
	assert := NewGomegaWithT(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Expect(r.URL.Path).To(Equal("/api/pipelines/p1/trigger"))
		assert.Expect(r.Method).To(Equal(http.MethodPost))

		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(client.TriggerResult{
			RunID:   "r1",
			Status:  "queued",
			Message: "ok",
		})
	}))
	defer srv.Close()

	c := client.New(srv.URL)
	result, err := c.TriggerPipeline("p1", client.TriggerRequest{})
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(result.RunID).To(Equal("r1"))
}

func TestTriggerPipelinePaused(t *testing.T) {
	assert := NewGomegaWithT(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()

	c := client.New(srv.URL)
	_, err := c.TriggerPipeline("p1", client.TriggerRequest{})
	assert.Expect(err).To(HaveOccurred())

	var pausedErr *client.PipelinePausedError
	assert.Expect(errors.As(err, &pausedErr)).To(BeTrue())
}

func TestTriggerPipelineRateLimit(t *testing.T) {
	assert := NewGomegaWithT(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := client.New(srv.URL)
	_, err := c.TriggerPipeline("p1", client.TriggerRequest{})
	assert.Expect(err).To(HaveOccurred())

	var rateLimitErr *client.RateLimitError
	assert.Expect(errors.As(err, &rateLimitErr)).To(BeTrue())
}

func TestPausePipeline(t *testing.T) {
	assert := NewGomegaWithT(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Expect(r.URL.Path).To(Equal("/api/pipelines/p1/pause"))
		assert.Expect(r.Method).To(Equal(http.MethodPost))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := client.New(srv.URL)
	err := c.PausePipeline("p1")
	assert.Expect(err).NotTo(HaveOccurred())
}

func TestUnpausePipeline(t *testing.T) {
	assert := NewGomegaWithT(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Expect(r.URL.Path).To(Equal("/api/pipelines/p1/unpause"))
		assert.Expect(r.Method).To(Equal(http.MethodPost))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := client.New(srv.URL)
	err := c.UnpausePipeline("p1")
	assert.Expect(err).NotTo(HaveOccurred())
}

func TestSeedJobPassed(t *testing.T) {
	assert := NewGomegaWithT(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Expect(r.URL.Path).To(Equal("/api/pipelines/p1/jobs/my-job/seed-passed"))
		assert.Expect(r.Method).To(Equal(http.MethodPost))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(client.SeedPassedResult{
			Job:   "my-job",
			RunID: "r1",
		})
	}))
	defer srv.Close()

	c := client.New(srv.URL)
	result, err := c.SeedJobPassed("p1", "my-job")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(result.RunID).To(Equal("r1"))
}

func TestListSchedules(t *testing.T) {
	assert := NewGomegaWithT(t)

	schedules := []storage.Schedule{
		{ID: "s1", Name: "nightly", ScheduleType: "cron", Enabled: true},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Expect(r.URL.Path).To(Equal("/api/pipelines/p1/schedules"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(schedules)
	}))
	defer srv.Close()

	c := client.New(srv.URL)
	got, err := c.ListSchedules("p1")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(got).To(HaveLen(1))
	assert.Expect(got[0].Name).To(Equal("nightly"))
}

func TestSetScheduleEnabled(t *testing.T) {
	assert := NewGomegaWithT(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Expect(r.URL.Path).To(Equal("/api/schedules/s1/enabled"))
		assert.Expect(r.Method).To(Equal(http.MethodPut))

		var body map[string]bool
		_ = json.NewDecoder(r.Body).Decode(&body)
		assert.Expect(body["enabled"]).To(BeTrue())

		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := client.New(srv.URL)
	err := c.SetScheduleEnabled("s1", true)
	assert.Expect(err).NotTo(HaveOccurred())
}

func TestBeginDeviceFlow(t *testing.T) {
	assert := NewGomegaWithT(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Expect(r.URL.Path).To(Equal("/auth/cli/begin"))
		assert.Expect(r.Method).To(Equal(http.MethodPost))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(client.DeviceFlowResult{
			Code:     "ABC123",
			LoginURL: "http://example.com/login",
		})
	}))
	defer srv.Close()

	c := client.New(srv.URL)
	result, err := c.BeginDeviceFlow()
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(result.Code).To(Equal("ABC123"))
}

func TestPollDeviceFlowPending(t *testing.T) {
	assert := NewGomegaWithT(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := client.New(srv.URL)
	_, done, err := c.PollDeviceFlow("code")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(done).To(BeFalse())
}

func TestPollDeviceFlowSuccess(t *testing.T) {
	assert := NewGomegaWithT(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "mytoken"})
	}))
	defer srv.Close()

	c := client.New(srv.URL)
	token, done, err := c.PollDeviceFlow("code")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(done).To(BeTrue())
	assert.Expect(token).To(Equal("mytoken"))
}

func TestPollDeviceFlowExpired(t *testing.T) {
	assert := NewGomegaWithT(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer srv.Close()

	c := client.New(srv.URL)
	_, _, err := c.PollDeviceFlow("code")
	assert.Expect(err).To(HaveOccurred())
	assert.Expect(err.Error()).To(ContainSubstring("expired"))
}

func TestAuthRequiredError(t *testing.T) {
	assert := NewGomegaWithT(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := client.New(srv.URL)
	_, err := c.ListPipelines()
	assert.Expect(err).To(HaveOccurred())

	var authErr *client.AuthRequiredError
	assert.Expect(errors.As(err, &authErr)).To(BeTrue())
	assert.Expect(authErr.ServerURL).To(Equal(srv.URL))
}

func TestAccessDeniedError(t *testing.T) {
	assert := NewGomegaWithT(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := client.New(srv.URL)
	_, err := c.ListPipelines()
	assert.Expect(err).To(HaveOccurred())

	var accessErr *client.AccessDeniedError
	assert.Expect(errors.As(err, &accessErr)).To(BeTrue())
}

func TestAuthToken(t *testing.T) {
	assert := NewGomegaWithT(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Expect(r.Header.Get("Authorization")).To(Equal("Bearer mytoken"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(storage.PaginationResult[storage.Pipeline]{})
	}))
	defer srv.Close()

	c := client.New(srv.URL, client.WithAuthToken("mytoken"))
	_, err := c.ListPipelines()
	assert.Expect(err).NotTo(HaveOccurred())
}

func TestBasicAuthFromURL(t *testing.T) {
	assert := NewGomegaWithT(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		assert.Expect(ok).To(BeTrue())
		assert.Expect(user).To(Equal("admin"))
		assert.Expect(pass).To(Equal("secret"))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(storage.PaginationResult[storage.Pipeline]{})
	}))
	defer srv.Close()

	// Construct URL with embedded credentials pointing to the test server.
	// The test server URL is http://127.0.0.1:PORT, so we embed creds.
	serverURL := "http://admin:secret@" + srv.Listener.Addr().String()
	c := client.New(serverURL)
	_, err := c.ListPipelines()
	assert.Expect(err).NotTo(HaveOccurred())
}

func TestTimeout(t *testing.T) {
	assert := NewGomegaWithT(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := client.New(srv.URL, client.WithTimeout(100*time.Millisecond))
	_, err := c.ListPipelines()
	assert.Expect(err).To(HaveOccurred())
}
