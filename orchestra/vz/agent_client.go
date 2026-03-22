//go:build darwin

package vz

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/jtarchie/pocketci/orchestra/vz/agent"
)

// AgentClient communicates with the vsock guest agent running inside the VM.
type AgentClient struct {
	conn net.Conn
	mu   sync.Mutex

	// connectFn creates a new connection to the agent.
	connectFn func() (net.Conn, error)
}

// NewAgentClient creates a new agent client using the provided connection
// and a function to create new connections for reconnection.
func NewAgentClient(conn net.Conn, connectFn func() (net.Conn, error)) *AgentClient {
	return &AgentClient{
		conn:      conn,
		connectFn: connectFn,
	}
}

// reconnect closes the existing connection and creates a new one.
func (c *AgentClient) reconnect() error {
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}

	conn, err := c.connectFn()
	if err != nil {
		return fmt.Errorf("failed to reconnect to agent: %w", err)
	}

	c.conn = conn

	return nil
}

// runOnce sends a request and reads one response, without retry.
func (c *AgentClient) runOnce(req agent.Request) (*agent.Response, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	data = append(data, '\n')

	if err := c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return nil, fmt.Errorf("failed to set write deadline: %w", err)
	}

	if _, err := c.conn.Write(data); err != nil {
		return nil, fmt.Errorf("failed to write request: %w", err)
	}

	if err := c.conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return nil, fmt.Errorf("failed to set read deadline: %w", err)
	}

	decoder := json.NewDecoder(c.conn)

	var resp agent.Response
	if err := decoder.Decode(&resp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if !resp.OK {
		return nil, fmt.Errorf("agent error: %s", resp.Error)
	}

	return &resp, nil
}

// run sends a request with auto-reconnection on failure.
func (c *AgentClient) run(req agent.Request) (*agent.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	resp, err := c.runOnce(req)
	if err == nil {
		return resp, nil
	}

	// Wait briefly before reconnecting
	time.Sleep(2 * time.Second)

	if reconnErr := c.reconnect(); reconnErr != nil {
		return nil, fmt.Errorf("original error: %w; reconnect failed: %w", err, reconnErr)
	}

	return c.runOnce(req)
}

// Ping checks if the guest agent is responsive.
func (c *AgentClient) Ping() error {
	_, err := c.run(agent.Request{Type: "ping"})
	return err
}

// Exec executes a command inside the guest.
// Returns the PID of the spawned process.
func (c *AgentClient) Exec(path string, args, env []string, stdinData []byte) (int, error) {
	req := agent.Request{
		Type: "exec",
		Path: path,
		Args: args,
		Env:  env,
	}

	if len(stdinData) > 0 {
		req.StdinData = base64.StdEncoding.EncodeToString(stdinData)
	}

	resp, err := c.run(req)
	if err != nil {
		return 0, fmt.Errorf("exec failed: %w", err)
	}

	return resp.PID, nil
}

// ExecStatusResult holds the result of a process status query.
type ExecStatusResult struct {
	Exited   bool
	ExitCode int
	OutData  string
	ErrData  string
}

// ExecStatus queries the status of a previously started process.
func (c *AgentClient) ExecStatus(pid int) (*ExecStatusResult, error) {
	resp, err := c.run(agent.Request{
		Type: "exec-status",
		PID:  pid,
	})
	if err != nil {
		return nil, fmt.Errorf("exec-status failed: %w", err)
	}

	return &ExecStatusResult{
		Exited:   resp.Exited,
		ExitCode: resp.ExitCode,
		OutData:  resp.Stdout,
		ErrData:  resp.Stderr,
	}, nil
}

// Close closes the agent connection.
func (c *AgentClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		return c.conn.Close()
	}

	return nil
}
