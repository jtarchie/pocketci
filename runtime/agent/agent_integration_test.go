package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtarchie/pocketci/orchestra/docker"
	pipelinerunner "github.com/jtarchie/pocketci/runtime/runner"
	"github.com/jtarchie/pocketci/storage"
	_ "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func newTestStorage(t *testing.T) storage.Driver {
	t.Helper()

	assert := NewGomegaWithT(t)

	initStorage, found := storage.GetFromDSN("sqlite://:memory:")
	assert.Expect(found).To(BeTrue(), "sqlite storage driver not registered")

	st, err := initStorage("sqlite://:memory:", "", slog.Default())
	assert.Expect(err).NotTo(HaveOccurred())

	t.Cleanup(func() { _ = st.Close() })

	return st
}

func newSequencedLLMServer(t *testing.T, responses []string) (*httptest.Server, *int32) {
	t.Helper()

	var reqCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		n := int(atomic.AddInt32(&reqCount, 1))
		idx := n - 1
		if idx >= len(responses) {
			idx = len(responses) - 1
		}

		_, _ = w.Write([]byte(responses[idx]))
	}))

	t.Cleanup(server.Close)

	return server, &reqCount
}

func configureFakeOpenAI(t *testing.T, baseURL string) {
	t.Helper()

	origBaseURL := defaultBaseURLs["openai"]
	defaultBaseURLs["openai"] = baseURL + "/v1"
	t.Cleanup(func() { defaultBaseURLs["openai"] = origBaseURL })
	t.Setenv("OPENAI_API_KEY", "test-key")
}

func newDockerRunner(t *testing.T, prefix string) *pipelinerunner.PipelineRunner {
	t.Helper()

	logger := slog.Default()
	namespace := fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	runID := prefix + "-run"

	driver, err := docker.NewDocker(namespace, logger, nil)
	if err != nil {
		t.Fatalf("new docker driver: %v", err)
	}

	t.Cleanup(func() { _ = driver.Close() })

	runner := pipelinerunner.NewPipelineRunner(context.Background(), driver, nil, logger, namespace, runID)
	t.Cleanup(func() { _ = runner.CleanupVolumes() })

	return runner
}

func mustCreateVolume(t *testing.T, runner *pipelinerunner.PipelineRunner, name string) pipelinerunner.VolumeResult {
	t.Helper()

	vol, err := runner.CreateVolume(pipelinerunner.VolumeInput{Name: name})
	if err != nil {
		t.Fatalf("create volume %q: %v", name, err)
	}

	return *vol
}

func mustRun(t *testing.T, runner *pipelinerunner.PipelineRunner, input pipelinerunner.RunInput) *pipelinerunner.RunResult {
	t.Helper()

	result, err := runner.Run(input)
	if err != nil {
		t.Fatalf("run %q: %v", input.Name, err)
	}

	return result
}

func seedDiffVolume(t *testing.T, runner *pipelinerunner.PipelineRunner, diffVol pipelinerunner.VolumeResult) {
	t.Helper()

	result := mustRun(t, runner, pipelinerunner.RunInput{
		Name:  "seed-diff",
		Image: "busybox",
		Mounts: map[string]pipelinerunner.VolumeResult{
			"diff": diffVol,
		},
		Command: struct {
			Path string   `json:"path"`
			Args []string `json:"args"`
			User string   `json:"user"`
		}{
			Path: "sh",
			Args: []string{"-c", "printf 'diff --git a/a b/b\\n+added line\\n' > diff/pr.diff"},
		},
	})

	if result.Code != 0 {
		t.Fatalf("seed diff failed with exit code %d: %s", result.Code, result.Stderr)
	}
}

func readResultArtifact(t *testing.T, runner *pipelinerunner.PipelineRunner, outputVol pipelinerunner.VolumeResult, taskName string) map[string]string {
	t.Helper()

	result := mustRun(t, runner, pipelinerunner.RunInput{
		Name:  taskName,
		Image: "busybox",
		Mounts: map[string]pipelinerunner.VolumeResult{
			"final-review": outputVol,
		},
		Command: struct {
			Path string   `json:"path"`
			Args []string `json:"args"`
			User string   `json:"user"`
		}{
			Path: "cat",
			Args: []string{"final-review/result.json"},
		},
	})

	if result.Code != 0 {
		t.Fatalf("read result artifact failed with exit code %d: %s", result.Code, result.Stderr)
	}

	var artifact map[string]string
	if err := json.Unmarshal([]byte(result.Stdout), &artifact); err != nil {
		t.Fatalf("unmarshal result artifact: %v", err)
	}

	return artifact
}

func TestRunAgent_FakeLLM_RealDocker(t *testing.T) {
	assert := NewGomegaWithT(t)

	responses := []string{
		`{
			"id":"chatcmpl-1",
			"object":"chat.completion",
			"created":1730000000,
			"model":"fake-model",
			"choices":[{
				"index":0,
				"message":{
					"role":"assistant",
					"content":"",
					"tool_calls":[{
						"id":"call_ls",
						"type":"function",
						"function":{
							"name":"run_command",
							"arguments":"{\"command\":\"/bin/sh\",\"args\":[\"-c\",\"ls diff\"]}"
						}
					}]
				},
				"finish_reason":"tool_calls"
			}],
			"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}
		}`,
		`{
			"id":"chatcmpl-2",
			"object":"chat.completion",
			"created":1730000001,
			"model":"fake-model",
			"choices":[{
				"index":0,
				"message":{
					"role":"assistant",
					"content":"",
					"tool_calls":[{
						"id":"call_cat",
						"type":"function",
						"function":{
							"name":"run_command",
							"arguments":"{\"command\":\"/bin/sh\",\"args\":[\"-c\",\"cat diff/pr.diff\"]}"
						}
					}]
				},
				"finish_reason":"tool_calls"
			}],
			"usage":{"prompt_tokens":30,"completion_tokens":6,"total_tokens":36}
		}`,
		`{
			"id":"chatcmpl-3",
			"object":"chat.completion",
			"created":1730000002,
			"model":"fake-model",
			"choices":[{
				"index":0,
				"message":{
					"role":"assistant",
					"content":"## Code Review\n\n### Summary\nFound diff file and successfully read content."
				},
				"finish_reason":"stop"
			}],
			"usage":{"prompt_tokens":40,"completion_tokens":10,"total_tokens":50}
		}`,
	}

	llm, reqCount := newSequencedLLMServer(t, responses)
	configureFakeOpenAI(t, llm.URL)

	runner := newDockerRunner(t, "agent-int")
	diffVol := mustCreateVolume(t, runner, "diff")
	outVol := mustCreateVolume(t, runner, "final-review")
	seedDiffVolume(t, runner, diffVol)

	result, err := RunAgent(context.Background(), runner, nil, "", AgentConfig{
		Name:   "final-reviewer",
		Prompt: "Use run_command to verify diff file via ls and cat, then summarize.",
		Model:  "openai/fake-model",
		Image:  "busybox",
		Mounts: map[string]pipelinerunner.VolumeResult{
			"diff":         diffVol,
			"final-review": outVol,
		},
		// Intentionally pass host-like path to verify path resolution logic.
		OutputVolumePath: outVol.Path,
	})
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(result).NotTo(BeNil())
	assert.Expect(result.Text).To(ContainSubstring("Found diff file"))
	assert.Expect(atomic.LoadInt32(reqCount)).To(BeNumerically(">=", 3))

	var sawLS bool
	var sawCat bool
	for _, event := range result.AuditLog {
		if event.Type != "tool_response" || event.ToolName != "run_command" || event.ToolResult == nil {
			continue
		}

		stdout, _ := event.ToolResult["stdout"].(string)
		if strings.Contains(stdout, "pr.diff") {
			sawLS = true
		}
		if strings.Contains(stdout, "added line") {
			sawCat = true
		}
	}

	if !sawLS || !sawCat {
		auditJSON, _ := json.MarshalIndent(result.AuditLog, "", "  ")
		t.Fatalf("expected ls/cat evidence in tool responses (sawLS=%v sawCat=%v)\nAuditLog:\n%s", sawLS, sawCat, string(auditJSON))
	}

	assert.Expect(sawLS).To(BeTrue())
	assert.Expect(sawCat).To(BeTrue())

	artifact := readResultArtifact(t, runner, outVol, "read-result")
	assert.Expect(artifact["status"]).To(Equal("success"))
	assert.Expect(artifact["text"]).To(ContainSubstring("Found diff file"))
}

// TestRunAgent_FakeLLM_RunScript_RealDocker verifies that the run_script tool
// executes a multi-line script in a single round-trip and that the audit log
// records one tool_call (not two) even though two commands run in the script.
func TestRunAgent_FakeLLM_RunScript_RealDocker(t *testing.T) {
	assert := NewGomegaWithT(t)

	responses := []string{
		// Turn 1: agent calls run_script with a two-step script.
		`{
			"id":"chatcmpl-rs-1",
			"object":"chat.completion",
			"created":1730000100,
			"model":"fake-model",
			"choices":[{
				"index":0,
				"message":{
					"role":"assistant",
					"content":"",
					"tool_calls":[{
						"id":"call_script",
						"type":"function",
						"function":{
							"name":"run_script",
							"arguments":"{\"script\":\"set -e\\nls diff\\ncat diff/pr.diff\"}"
						}
					}]
				},
				"finish_reason":"tool_calls"
			}],
			"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}
		}`,
		// Turn 2: agent summarizes after receiving the combined output.
		`{
			"id":"chatcmpl-rs-2",
			"object":"chat.completion",
			"created":1730000101,
			"model":"fake-model",
			"choices":[{
				"index":0,
				"message":{
					"role":"assistant",
					"content":"Script ran successfully: found diff and read content in one call."
				},
				"finish_reason":"stop"
			}],
			"usage":{"prompt_tokens":30,"completion_tokens":8,"total_tokens":38}
		}`,
	}

	llm, reqCount := newSequencedLLMServer(t, responses)
	configureFakeOpenAI(t, llm.URL)

	runner := newDockerRunner(t, "agent-script")
	diffVol := mustCreateVolume(t, runner, "diff")
	outVol := mustCreateVolume(t, runner, "final-review")
	seedDiffVolume(t, runner, diffVol)

	result, err := RunAgent(context.Background(), runner, nil, "", AgentConfig{
		Name:   "script-agent",
		Prompt: "Use run_script to list and read diff/pr.diff in one call, then summarize.",
		Model:  "openai/fake-model",
		Image:  "busybox",
		Mounts: map[string]pipelinerunner.VolumeResult{
			"diff":         diffVol,
			"final-review": outVol,
		},
		OutputVolumePath: outVol.Path,
	})
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(result).NotTo(BeNil())
	assert.Expect(result.Text).To(ContainSubstring("one call"))

	// Exactly two LLM requests: one tool call, one final answer.
	assert.Expect(atomic.LoadInt32(reqCount)).To(BeNumerically("==", 2))

	// Audit log must show exactly one run_script tool_call.
	var scriptCalls int
	var combinedOutput string
	for _, event := range result.AuditLog {
		if event.Type == "tool_call" && event.ToolName == "run_script" {
			scriptCalls++
		}
		if event.Type == "tool_response" && event.ToolName == "run_script" && event.ToolResult != nil {
			combinedOutput, _ = event.ToolResult["stdout"].(string)
		}
	}

	assert.Expect(scriptCalls).To(Equal(1), "expected exactly one run_script tool call")
	// Both ls output and diff content must appear in the single response.
	assert.Expect(combinedOutput).To(ContainSubstring("pr.diff"))
	assert.Expect(combinedOutput).To(ContainSubstring("added line"))

	artifact := readResultArtifact(t, runner, outVol, "read-script-result")
	assert.Expect(artifact["status"]).To(Equal("success"))
}

func TestRunAgent_FakeLLM_InvalidToolArgs_RealDocker(t *testing.T) {
	assert := NewGomegaWithT(t)

	responses := []string{
		`{
			"id":"chatcmpl-invalid",
			"object":"chat.completion",
			"created":1730000010,
			"model":"fake-model",
			"choices":[{
				"index":0,
				"message":{
					"role":"assistant",
					"content":"",
					"tool_calls":[{
						"id":"call_invalid",
						"type":"function",
						"function":{
							"name":"run_command",
							"arguments":"{\"args\":[\"-c\",\"ls\"]}"
						}
					}]
				},
				"finish_reason":"tool_calls"
			}],
			"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}
		}`,
		`{
			"id":"chatcmpl-invalid-final",
			"object":"chat.completion",
			"created":1730000011,
			"model":"fake-model",
			"choices":[{
				"index":0,
				"message":{
					"role":"assistant",
					"content":"Tool arguments were invalid, but audit captured the error."
				},
				"finish_reason":"stop"
			}],
			"usage":{"prompt_tokens":25,"completion_tokens":8,"total_tokens":33}
		}`,
	}

	llm, _ := newSequencedLLMServer(t, responses)
	configureFakeOpenAI(t, llm.URL)

	runner := newDockerRunner(t, "agent-int-invalid")
	outVol := mustCreateVolume(t, runner, "final-review")

	result, err := RunAgent(context.Background(), runner, nil, "", AgentConfig{
		Name:   "final-reviewer",
		Prompt: "Try to run ls and summarize the result.",
		Model:  "openai/fake-model",
		Image:  "busybox",
		Mounts: map[string]pipelinerunner.VolumeResult{
			"final-review": outVol,
		},
		OutputVolumePath: outVol.Path,
	})
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(result).NotTo(BeNil())

	var validationErr string
	for _, event := range result.AuditLog {
		if event.Type != "tool_response" || event.ToolName != "run_command" || event.ToolResult == nil {
			continue
		}

		if errText, ok := event.ToolResult["error"].(string); ok {
			validationErr = errText

			break
		}
	}

	assert.Expect(validationErr).To(ContainSubstring("missing properties"))

	artifact := readResultArtifact(t, runner, outVol, "read-invalid-result")
	assert.Expect(artifact["status"]).To(Equal("success"))
	assert.Expect(strings.TrimSpace(artifact["text"])).NotTo(BeEmpty())
}

// TestRunAgent_ContextFiles_RealDocker consolidates context file pre-injection
// tests into subtests sharing a single Docker runner.
func TestRunAgent_ContextFiles_RealDocker(t *testing.T) {
	runner := newDockerRunner(t, "agent-ctx-files")

	t.Run("pre_injection", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		responses := []string{
			`{
				"id":"chatcmpl-cf-1","object":"chat.completion","created":1730000200,"model":"fake-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"The diff shows: added line"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":50,"completion_tokens":10,"total_tokens":60}
			}`,
		}

		llm, reqCount := newSequencedLLMServer(t, responses)
		configureFakeOpenAI(t, llm.URL)

		diffVol := mustCreateVolume(t, runner, "diff-pre")
		outVol := mustCreateVolume(t, runner, "out-pre")
		seedDiffVolume(t, runner, diffVol)

		result, err := RunAgent(context.Background(), runner, nil, "", AgentConfig{
			Name:   "file-context-agent",
			Prompt: "Summarize the diff content already injected in your context.",
			Model:  "openai/fake-model",
			Image:  "busybox",
			Mounts: map[string]pipelinerunner.VolumeResult{
				"diff-pre": diffVol,
				"out-pre":  outVol,
			},
			OutputVolumePath: outVol.Path,
			Context: &AgentContext{
				Files: []AgentContextFile{
					{Path: "diff-pre/pr.diff"},
				},
			},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result).NotTo(BeNil())
		assert.Expect(result.Text).To(ContainSubstring("added line"))
		assert.Expect(atomic.LoadInt32(reqCount)).To(BeNumerically("==", 1))

		var preContextEvent *AuditEvent
		for i := range result.AuditLog {
			if result.AuditLog[i].Type == "pre_context" && result.AuditLog[i].ToolName == "read_file" {
				preContextEvent = &result.AuditLog[i]
				break
			}
		}

		assert.Expect(preContextEvent).NotTo(BeNil(), "expected a pre_context read_file audit event")
		assert.Expect(preContextEvent.ToolArgs).To(HaveKeyWithValue("path", "diff-pre/pr.diff"))
		content, _ := preContextEvent.ToolResult["content"].(string)
		assert.Expect(content).To(ContainSubstring("added line"))

		assert.Expect(result.Usage.ToolCallCount).To(BeZero())
		assert.Expect(result.Usage.LLMRequests).To(BeNumerically("==", 1))
	})

	t.Run("llm_receives_content", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		var (
			mu          sync.Mutex
			capturedReq string
		)

		llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			capturedReq = string(body)
			mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl-cap-1","object":"chat.completion","created":1730000300,"model":"fake-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"Diff reviewed."},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":80,"completion_tokens":5,"total_tokens":85}
			}`))
		}))
		t.Cleanup(llm.Close)
		configureFakeOpenAI(t, llm.URL)

		diffVol := mustCreateVolume(t, runner, "diff-llm")
		outVol := mustCreateVolume(t, runner, "out-llm")
		seedDiffVolume(t, runner, diffVol)

		_, err := RunAgent(context.Background(), runner, nil, "", AgentConfig{
			Name:   "cf-llm-agent",
			Prompt: "Summarize the diff.",
			Model:  "openai/fake-model",
			Image:  "busybox",
			Mounts: map[string]pipelinerunner.VolumeResult{
				"diff-llm": diffVol,
				"out-llm":  outVol,
			},
			OutputVolumePath: outVol.Path,
			Context: &AgentContext{
				Files: []AgentContextFile{
					{Path: "diff-llm/pr.diff"},
				},
			},
		})
		assert.Expect(err).NotTo(HaveOccurred())

		mu.Lock()
		body := capturedReq
		mu.Unlock()

		assert.Expect(body).To(ContainSubstring("added line"),
			"LLM request must contain the pre-injected diff content")

		promptIdx := strings.Index(body, "Summarize the diff.")
		fileIdx := strings.Index(body, "added line")
		assert.Expect(promptIdx).To(BeNumerically(">", -1), "user prompt missing from LLM request")
		assert.Expect(promptIdx).To(BeNumerically("<", fileIdx),
			"user prompt must appear before pre-injected file content in LLM messages")
	})
}

func TestRunAgent_ContextTasksPreInjection_RealDocker(t *testing.T) {
	assert := NewGomegaWithT(t)

	responses := []string{
		`{
			"id":"chatcmpl-context",
			"object":"chat.completion",
			"created":1730000020,
			"model":"fake-model",
			"choices":[{
				"index":0,
				"message":{
					"role":"assistant",
					"content":"Used pre-injected context successfully."
				},
				"finish_reason":"stop"
			}],
			"usage":{"prompt_tokens":30,"completion_tokens":7,"total_tokens":37}
		}`,
	}

	llm, _ := newSequencedLLMServer(t, responses)
	configureFakeOpenAI(t, llm.URL)

	runner := newDockerRunner(t, "agent-int-context")
	outVol := mustCreateVolume(t, runner, "final-review")

	st := newTestStorage(t)
	runID := "context-run"
	base := "/pipeline/" + runID + "/jobs/review-pr"

	_ = st.Set(context.Background(), base+"/1/agent/code-quality-reviewer", storage.Payload{
		"status": "success",
		"stdout": "- cq issue",
	})
	_ = st.Set(context.Background(), base+"/2/agent/security-reviewer", storage.Payload{
		"status": "success",
		"stdout": "- sec issue",
	})
	_ = st.Set(context.Background(), base+"/3/agent/maintainability-reviewer", storage.Payload{
		"status": "success",
		"stdout": "- maint issue",
	})

	result, err := RunAgent(context.Background(), runner, nil, "", AgentConfig{
		Name:   "final-reviewer",
		Prompt: "Summarize prior reviews.",
		Model:  "openai/fake-model",
		Image:  "busybox",
		Mounts: map[string]pipelinerunner.VolumeResult{
			"final-review": outVol,
		},
		OutputVolumePath: outVol.Path,
		Storage:          st,
		RunID:            runID,
		Context: &AgentContext{
			Tasks: []AgentContextTask{
				{Name: "code-quality-reviewer", Field: "stdout"},
				{Name: "security-reviewer", Field: "stdout"},
				{Name: "maintainability-reviewer", Field: "stdout"},
			},
		},
	})
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(result).NotTo(BeNil())

	var preContextListTasks bool
	var injectedTaskCount int
	for _, event := range result.AuditLog {
		if event.Type != "pre_context" {
			continue
		}

		if event.ToolName == "list_tasks" {
			preContextListTasks = true
		}

		if event.ToolName == "get_task_result" {
			injectedTaskCount++
		}
	}

	assert.Expect(preContextListTasks).To(BeTrue())
	assert.Expect(injectedTaskCount).To(Equal(3))

	artifact := readResultArtifact(t, runner, outVol, "read-context-result")
	assert.Expect(artifact["status"]).To(Equal("success"))
	assert.Expect(strings.TrimSpace(artifact["text"])).NotTo(BeEmpty())
}

// TestRunAgent_Validation_RealDocker consolidates all validation and follow-up
// scenarios into subtests sharing a single Docker runner.
func TestRunAgent_Validation_RealDocker(t *testing.T) {
	runner := newDockerRunner(t, "agent-val")

	t.Run("passes_no_followup", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		responses := []string{
			`{
				"id":"chatcmpl-vp-1","object":"chat.completion","created":1730000400,"model":"fake-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"{\"summary\":\"All good\",\"issues\":[]}"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":20,"completion_tokens":10,"total_tokens":30}
			}`,
		}

		llm, reqCount := newSequencedLLMServer(t, responses)
		configureFakeOpenAI(t, llm.URL)

		outVol := mustCreateVolume(t, runner, "val-pass")

		result, err := RunAgent(context.Background(), runner, nil, "", AgentConfig{
			Name:             "val-pass-agent",
			Prompt:           "Output JSON.",
			Model:            "openai/fake-model",
			Image:            "busybox",
			Mounts:           map[string]pipelinerunner.VolumeResult{"val-pass": outVol},
			OutputVolumePath: outVol.Path,
			Validation: &AgentValidationConfig{
				Expr: `text != "" && text contains "{"`,
			},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result).NotTo(BeNil())
		assert.Expect(result.Text).To(ContainSubstring(`"summary"`))

		for _, event := range result.AuditLog {
			assert.Expect(event.Type).NotTo(Equal("validation_followup"),
				"expected no validation_followup when validation passes")
		}

		assert.Expect(atomic.LoadInt32(reqCount)).To(BeNumerically(">=", 1))
	})

	t.Run("fails_triggers_followup", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		responses := []string{
			`{
				"id":"chatcmpl-vf-1","object":"chat.completion","created":1730000500,"model":"fake-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"I found several issues in the code."},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":20,"completion_tokens":8,"total_tokens":28}
			}`,
			`{
				"id":"chatcmpl-vf-2","object":"chat.completion","created":1730000501,"model":"fake-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"{\"summary\":\"Issues found\",\"issues\":[{\"severity\":\"high\"}]}"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":30,"completion_tokens":15,"total_tokens":45}
			}`,
		}

		llm, reqCount := newSequencedLLMServer(t, responses)
		configureFakeOpenAI(t, llm.URL)

		outVol := mustCreateVolume(t, runner, "val-fail")

		result, err := RunAgent(context.Background(), runner, nil, "", AgentConfig{
			Name:             "val-fail-agent",
			Prompt:           "Output JSON review.",
			Model:            "openai/fake-model",
			Image:            "busybox",
			Mounts:           map[string]pipelinerunner.VolumeResult{"val-fail": outVol},
			OutputVolumePath: outVol.Path,
			Validation: &AgentValidationConfig{
				Expr: `text contains "{"`,
			},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result).NotTo(BeNil())
		assert.Expect(result.Text).To(ContainSubstring(`"summary"`))
		assert.Expect(atomic.LoadInt32(reqCount)).To(BeNumerically("==", 2))

		var followUpEvent *AuditEvent
		for i := range result.AuditLog {
			if result.AuditLog[i].Type == "validation_followup" {
				followUpEvent = &result.AuditLog[i]
				break
			}
		}

		assert.Expect(followUpEvent).NotTo(BeNil(), "expected a validation_followup audit event")
		assert.Expect(followUpEvent.Text).To(ContainSubstring("final text response"))

		artifact := readResultArtifact(t, runner, outVol, "read-val-fail")
		assert.Expect(artifact["status"]).To(Equal("success"))
	})

	t.Run("fails_custom_prompt", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		customPrompt := "You MUST output strict JSON with summary and issues fields."

		responses := []string{
			`{
				"id":"chatcmpl-vfc-1","object":"chat.completion","created":1730000600,"model":"fake-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"Plain text review."},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}
			}`,
			`{
				"id":"chatcmpl-vfc-2","object":"chat.completion","created":1730000601,"model":"fake-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"{\"summary\":\"Fixed\",\"issues\":[]}"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":30,"completion_tokens":10,"total_tokens":40}
			}`,
		}

		llm, _ := newSequencedLLMServer(t, responses)
		configureFakeOpenAI(t, llm.URL)

		outVol := mustCreateVolume(t, runner, "val-custom")

		result, err := RunAgent(context.Background(), runner, nil, "", AgentConfig{
			Name:             "val-custom-agent",
			Prompt:           "Output JSON review.",
			Model:            "openai/fake-model",
			Image:            "busybox",
			Mounts:           map[string]pipelinerunner.VolumeResult{"val-custom": outVol},
			OutputVolumePath: outVol.Path,
			Validation: &AgentValidationConfig{
				Expr:   `text contains "{"`,
				Prompt: customPrompt,
			},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result).NotTo(BeNil())

		var followUpEvent *AuditEvent
		for i := range result.AuditLog {
			if result.AuditLog[i].Type == "validation_followup" {
				followUpEvent = &result.AuditLog[i]
				break
			}
		}

		assert.Expect(followUpEvent).NotTo(BeNil())
		assert.Expect(followUpEvent.Text).To(Equal(customPrompt))
	})

	t.Run("expr_error_triggers_followup", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		responses := []string{
			`{
				"id":"chatcmpl-ve-1","object":"chat.completion","created":1730000700,"model":"fake-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"Some output."},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}
			}`,
			`{
				"id":"chatcmpl-ve-2","object":"chat.completion","created":1730000701,"model":"fake-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"Corrected output after error."},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":25,"completion_tokens":6,"total_tokens":31}
			}`,
		}

		llm, reqCount := newSequencedLLMServer(t, responses)
		configureFakeOpenAI(t, llm.URL)

		outVol := mustCreateVolume(t, runner, "val-err")

		result, err := RunAgent(context.Background(), runner, nil, "", AgentConfig{
			Name:             "val-err-agent",
			Prompt:           "Output something.",
			Model:            "openai/fake-model",
			Image:            "busybox",
			Mounts:           map[string]pipelinerunner.VolumeResult{"val-err": outVol},
			OutputVolumePath: outVol.Path,
			Validation: &AgentValidationConfig{
				Expr: `undefinedFunction(text)`,
			},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result).NotTo(BeNil())
		assert.Expect(atomic.LoadInt32(reqCount)).To(BeNumerically("==", 2))

		var sawError, sawFollowUp bool
		for _, event := range result.AuditLog {
			if event.Type == "validation_error" {
				sawError = true
				assert.Expect(event.Text).To(ContainSubstring("Validation expression error"))
			}
			if event.Type == "validation_followup" {
				sawFollowUp = true
			}
		}

		assert.Expect(sawError).To(BeTrue(), "expected validation_error audit event")
		assert.Expect(sawFollowUp).To(BeTrue(), "expected validation_followup audit event")
	})

	t.Run("default_followup_empty_text", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		responses := []string{
			`{
				"id":"chatcmpl-df-1","object":"chat.completion","created":1730000800,"model":"fake-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":20,"completion_tokens":1,"total_tokens":21}
			}`,
			`{
				"id":"chatcmpl-df-2","object":"chat.completion","created":1730000801,"model":"fake-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"Here is my complete response after follow-up."},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":30,"completion_tokens":8,"total_tokens":38}
			}`,
		}

		llm, reqCount := newSequencedLLMServer(t, responses)
		configureFakeOpenAI(t, llm.URL)

		outVol := mustCreateVolume(t, runner, "def-followup")

		result, err := RunAgent(context.Background(), runner, nil, "", AgentConfig{
			Name:             "def-followup-agent",
			Prompt:           "Produce a review.",
			Model:            "openai/fake-model",
			Image:            "busybox",
			Mounts:           map[string]pipelinerunner.VolumeResult{"def-followup": outVol},
			OutputVolumePath: outVol.Path,
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result).NotTo(BeNil())
		assert.Expect(result.Text).To(ContainSubstring("complete response after follow-up"))
		assert.Expect(atomic.LoadInt32(reqCount)).To(BeNumerically("==", 2))

		var sawFollowUp bool
		for _, event := range result.AuditLog {
			if event.Type == "validation_followup" {
				sawFollowUp = true
			}
		}

		assert.Expect(sawFollowUp).To(BeTrue(), "expected validation_followup for empty text default")

		artifact := readResultArtifact(t, runner, outVol, "read-def-followup")
		assert.Expect(artifact["status"]).To(Equal("success"))
		assert.Expect(artifact["text"]).To(ContainSubstring("complete response after follow-up"))
	})
}
