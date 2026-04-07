//go:build darwin && cgo

package vz

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/jtarchie/pocketci/orchestra"
)

// Container represents a command executed inside the Apple VZ guest via the vsock agent.
type Container struct {
	agent  *AgentClient
	pid    int
	taskID string

	mu       sync.Mutex
	done     bool
	exitCode int
	stdout   []byte
	stderr   []byte
}

// ID returns the task ID for this container.
func (c *Container) ID() string {
	return c.taskID
}

// Cleanup is a no-op for VZ containers — cleanup happens at driver Close.
func (c *Container) Cleanup(_ context.Context) error {
	return nil
}

// pollStatus queries the agent for process status and caches the result.
func (c *Container) pollStatus() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.done {
		return nil
	}

	result, err := c.agent.ExecStatus(c.pid)
	if err != nil {
		return fmt.Errorf("failed to poll status: %w", err)
	}

	if result.Exited {
		c.done = true
		c.exitCode = result.ExitCode

		if result.OutData != "" {
			decoded, err := base64.StdEncoding.DecodeString(result.OutData)
			if err == nil {
				c.stdout = decoded
			}
		}

		if result.ErrData != "" {
			decoded, err := base64.StdEncoding.DecodeString(result.ErrData)
			if err == nil {
				c.stderr = decoded
			}
		}
	}

	return nil
}

// Status returns the current status of the guest process.
func (c *Container) Status(_ context.Context) (orchestra.ContainerStatus, error) {
	err := c.pollStatus()
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	return &Status{
		isDone:   c.done,
		exitCode: c.exitCode,
	}, nil
}

// Logs retrieves container output. When follow is false, returns current output.
// When follow is true, polls until the process exits or context is cancelled.
func (c *Container) Logs(ctx context.Context, stdout, stderr io.Writer, follow bool) error {
	if follow {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return c.writeLogs(stdout, stderr)
			case <-ticker.C:
				err := c.pollStatus()
				if err != nil {
					return err
				}

				c.mu.Lock()
				done := c.done
				c.mu.Unlock()

				if done {
					return c.writeLogs(stdout, stderr)
				}
			}
		}
	}

	err := c.pollStatus()
	if err != nil {
		return err
	}

	return c.writeLogs(stdout, stderr)
}

func (c *Container) writeLogs(stdout, stderr io.Writer) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.stdout) > 0 {
		_, err := io.Copy(stdout, strings.NewReader(string(c.stdout)))
		if err != nil {
			return fmt.Errorf("failed to write stdout: %w", err)
		}
	}

	if len(c.stderr) > 0 {
		_, err := io.Copy(stderr, strings.NewReader(string(c.stderr)))
		if err != nil {
			return fmt.Errorf("failed to write stderr: %w", err)
		}
	}

	return nil
}

// Status represents the execution status of a guest process.
type Status struct {
	isDone   bool
	exitCode int
}

// IsDone returns whether the process has exited.
func (s *Status) IsDone() bool {
	return s.isDone
}

// ExitCode returns the exit code of the process.
func (s *Status) ExitCode() int {
	return s.exitCode
}
