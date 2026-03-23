package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/runtime/support"
)

// SandboxInput describes the sandbox container to create.
type SandboxInput struct {
	Image      string                  `json:"image"`
	Name       string                  `json:"name"`
	Env        map[string]string       `json:"env"`
	Mounts     map[string]VolumeResult `json:"mounts"`
	WorkDir    string                  `json:"work_dir"`
	Privileged bool                    `json:"privileged"`
}

// ExecInput describes a single command to run inside a sandbox.
type ExecInput struct {
	Command struct {
		Path string   `json:"path"`
		Args []string `json:"args"`
		User string   `json:"user"`
	} `json:"command"`
	Env     map[string]string `json:"env"`
	WorkDir string            `json:"work_dir"`
	Stdin   string            `json:"stdin"`
	Timeout string            `json:"timeout"`
	// OnOutput is called with streaming output chunks. Not serialised from JS.
	OnOutput OutputCallback `json:"-"`
}

// sandboxStreamWriter writes to a strings.Builder and optionally invokes
// an OutputCallback for each chunk. Satisfies io.Writer.
type sandboxStreamWriter struct {
	stream   string
	buf      *strings.Builder
	callback OutputCallback
}

func (w *sandboxStreamWriter) Write(p []byte) (n int, err error) {
	n, err = w.buf.Write(p)
	if err != nil || w.callback == nil || n == 0 {
		return
	}

	w.callback(w.stream, string(p[:n]))

	return
}

// SandboxHandle manages a long-lived sandbox container that accepts sequential
// exec calls. Obtain one via Runner.StartSandbox.
type SandboxHandle struct {
	sandbox orchestra.Sandbox
	runner  *PipelineRunner
	logger  *slog.Logger
}

// ID returns the driver-specific sandbox container identifier.
func (h *SandboxHandle) ID() string {
	return h.sandbox.ID()
}

// Exec runs a single command inside the sandbox.
// env and workDir apply only to this invocation; they do not persist.
func (h *SandboxHandle) Exec(ctx context.Context, input ExecInput) (*RunResult, error) {

	if input.Timeout != "" {
		timeout, err := time.ParseDuration(input.Timeout)
		if err != nil {
			return nil, fmt.Errorf("sandbox exec: invalid timeout: %w", err)
		}

		if timeout > 0 {
			var cancel context.CancelFunc

			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
	}

	// Resolve secret references in env (matching PipelineRunner.Run behaviour).
	env := input.Env
	if h.runner.secretsManager != nil && len(env) > 0 {
		env = h.resolveSecretEnv(ctx, env)
	}

	cmd := make([]string, 0, 1+len(input.Command.Args))
	cmd = append(cmd, input.Command.Path)
	cmd = append(cmd, input.Command.Args...)

	var stdinReader io.Reader
	if input.Stdin != "" {
		stdinReader = strings.NewReader(input.Stdin)
	}

	stdoutBuf := &strings.Builder{}
	stderrBuf := &strings.Builder{}

	stdoutWriter := &sandboxStreamWriter{stream: "stdout", buf: stdoutBuf, callback: input.OnOutput}
	stderrWriter := &sandboxStreamWriter{stream: "stderr", buf: stderrBuf, callback: input.OnOutput}

	status, err := h.sandbox.Exec(ctx, cmd, env, input.WorkDir, stdinReader, stdoutWriter, stderrWriter)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return &RunResult{Status: RunAbort}, nil
		}

		return nil, fmt.Errorf("sandbox exec: %w", err)
	}

	stdoutStr := stdoutBuf.String()
	stderrStr := stderrBuf.String()

	if len(h.runner.secretValues) > 0 {
		stdoutStr = support.RedactSecrets(stdoutStr, h.runner.secretValues)
		stderrStr = support.RedactSecrets(stderrStr, h.runner.secretValues)
	}

	return &RunResult{
		Code:   status.ExitCode(),
		Stdout: stdoutStr,
		Stderr: stderrStr,
		Status: RunComplete,
	}, nil
}

// Close shuts down the sandbox container.
func (h *SandboxHandle) Close() error {
	return h.sandbox.Cleanup(h.runner.ctx)
}

// resolveSecretEnv resolves "secret:..." references in env to actual values.
func (h *SandboxHandle) resolveSecretEnv(ctx context.Context, env map[string]string) map[string]string {
	var secretKeys []string

	for _, val := range env {
		if strings.HasPrefix(val, "secret:") {
			secretKeys = append(secretKeys, strings.TrimPrefix(val, "secret:"))
		}
	}

	if len(secretKeys) == 0 {
		return env
	}

	secretMap, err := h.runner.loadSecrets(ctx, secretKeys)
	if err != nil {
		return env
	}

	resolved := make(map[string]string, len(env))

	for k, v := range env {
		if !strings.HasPrefix(v, "secret:") {
			resolved[k] = v

			continue
		}

		secretKey := strings.TrimPrefix(v, "secret:")
		if secretVal, ok := secretMap[secretKey]; ok {
			resolved[k] = secretVal
			h.runner.secretValues = append(h.runner.secretValues, secretVal)

			continue
		}

		resolved[k] = v
	}

	return resolved
}

// StartSandbox creates and starts a long-lived sandbox container.
// The driver must implement orchestra.SandboxDriver.
func (c *PipelineRunner) StartSandbox(input SandboxInput) (*SandboxHandle, error) {
	sandboxDriver, ok := c.client.(orchestra.SandboxDriver)
	if !ok {
		return nil, fmt.Errorf("driver %q does not support sandbox mode", c.client.Name())
	}

	c.mu.Lock()
	stepID := fmt.Sprintf("%d-%s-sandbox", c.callIndex, input.Name)
	c.callIndex++
	c.mu.Unlock()

	taskID := support.DeterministicTaskID(c.namespace, c.runID, stepID, input.Name)

	var mounts orchestra.Mounts
	for path, volume := range input.Mounts {
		mounts = append(mounts, orchestra.Mount{
			Name: volume.Name,
			Path: path,
		})
	}

	task := orchestra.Task{
		ID:         taskID,
		Image:      input.Image,
		Env:        input.Env,
		Mounts:     mounts,
		Privileged: input.Privileged,
		WorkDir:    input.WorkDir,
	}

	sandbox, err := sandboxDriver.StartSandbox(c.ctx, task)
	if err != nil {
		return nil, fmt.Errorf("failed to start sandbox: %w", err)
	}

	return &SandboxHandle{
		sandbox: sandbox,
		runner:  c,
		logger:  c.logger,
	}, nil
}

// StartSandbox delegates to the inner PipelineRunner.
func (r *ResumableRunner) StartSandbox(input SandboxInput) (*SandboxHandle, error) {
	return r.runner.StartSandbox(input)
}
