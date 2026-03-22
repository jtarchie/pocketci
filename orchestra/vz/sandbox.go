//go:build darwin

package vz

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jtarchie/pocketci/orchestra"
)

// VZSandbox represents an Apple VZ VM sandbox that dispatches sequential exec
// calls via the vsock agent. The VM itself is kept alive by ensureVM.
type VZSandbox struct {
	agent  *AgentClient
	driver *VZ
}

var _ orchestra.Sandbox = (*VZSandbox)(nil)

// ID returns the driver identifier for this sandbox.
func (s *VZSandbox) ID() string {
	return s.driver.namespace
}

// Exec runs cmd inside the VZ guest and writes decoded output to stdout/stderr.
// env and workDir apply only to this invocation.
func (s *VZSandbox) Exec(
	ctx context.Context,
	cmd []string,
	env map[string]string,
	workDir string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) (orchestra.ContainerStatus, error) {
	envSlice := make([]string, 0, len(env))
	for k, v := range env {
		envSlice = append(envSlice, fmt.Sprintf("%s=%s", k, v))
	}

	execCmd := cmd
	if workDir != "" {
		execCmd = []string{"/bin/sh", "-c", "cd " + workDir + " && exec " + strings.Join(cmd, " ")}
	}

	var stdinData []byte

	if stdin != nil {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("sandbox exec: failed to read stdin: %w", err)
		}

		stdinData = b
	}

	pid, err := s.agent.Exec(execCmd[0], execCmd[1:], envSlice, stdinData)
	if err != nil {
		return nil, fmt.Errorf("sandbox exec: agent exec failed: %w", err)
	}

	// Poll until the process exits or the context is cancelled.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			result, err := s.agent.ExecStatus(pid)
			if err != nil {
				return nil, fmt.Errorf("sandbox exec: agent exec-status failed: %w", err)
			}

			if !result.Exited {
				continue
			}

			if result.OutData != "" {
				decoded, decErr := base64.StdEncoding.DecodeString(result.OutData)
				if decErr == nil {
					_, _ = io.Copy(stdout, strings.NewReader(string(decoded)))
				}
			}

			if result.ErrData != "" {
				decoded, decErr := base64.StdEncoding.DecodeString(result.ErrData)
				if decErr == nil {
					_, _ = io.Copy(stderr, strings.NewReader(string(decoded)))
				}
			}

			return &Status{
				isDone:   true,
				exitCode: result.ExitCode,
			}, nil
		}
	}
}

// Cleanup is a no-op for VZ sandboxes — VM lifecycle is managed by the driver.
func (s *VZSandbox) Cleanup(_ context.Context) error {
	return nil
}

// StartSandbox implements orchestra.SandboxDriver.
// The VZ VM is booted lazily on first use (ensureVM). The sandbox runs
// commands directly via the vsock agent without an idle process.
func (v *VZ) StartSandbox(ctx context.Context, task orchestra.Task) (orchestra.Sandbox, error) {
	if err := v.ensureVM(ctx); err != nil {
		return nil, fmt.Errorf("sandbox: failed to ensure VM: %w", err)
	}

	return &VZSandbox{
		agent:  v.agent,
		driver: v,
	}, nil
}
