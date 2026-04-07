package qemu

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
	"sync"
	"time"
)

// QGAClient communicates with the QEMU Guest Agent over a virtio-serial channel.
// Supports both Unix socket and TCP connections.
type QGAClient struct {
	conn    net.Conn
	mu      sync.Mutex
	network string // "unix" or "tcp"
	address string // socket path or host:port
}

type qgaRequest struct {
	Execute   string `json:"execute"`
	Arguments any    `json:"arguments,omitempty"`
}

type qgaResponse struct {
	Return json.RawMessage `json:"return,omitempty"`
	Error  *qgaError       `json:"error,omitempty"`
}

type qgaError struct {
	Class string `json:"class"`
	Desc  string `json:"desc"`
}

type guestExecArgs struct {
	Path          string   `json:"path"`
	Arg           []string `json:"arg,omitempty"`
	Env           []string `json:"env,omitempty"`
	InputData     string   `json:"input-data,omitempty"`
	CaptureOutput bool     `json:"capture-output"`
}

type guestExecResult struct {
	PID int `json:"pid"`
}

// GuestExecStatusResult holds the result of a guest-exec-status call.
type GuestExecStatusResult struct {
	Exited       bool   `json:"exited"`
	ExitCode     int    `json:"exitcode"`
	Signal       int    `json:"signal"`
	OutData      string `json:"out-data"`
	ErrData      string `json:"err-data"`
	OutTruncated bool   `json:"out-truncated"`
	ErrTruncated bool   `json:"err-truncated"`
}

type guestSyncArgs struct {
	ID int64 `json:"id"`
}

// NewQGAClient connects to the QEMU Guest Agent via the given network and address.
// Supports "unix" (socket path) or "tcp" (host:port).
func NewQGAClient(network, address string) (*QGAClient, error) {
	client := &QGAClient{network: network, address: address}

	err := client.connect()
	if err != nil {
		return nil, err
	}

	return client, nil
}

// connect establishes a connection and performs the QGA handshake.
func (c *QGAClient) connect() error {
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}

	conn, err := net.DialTimeout(c.network, c.address, 5*time.Second)
	if err != nil {
		return fmt.Errorf("failed to connect to QGA at %s/%s: %w", c.network, c.address, err)
	}

	c.conn = conn

	syncErr := c.sync()
	if syncErr != nil {
		_ = c.conn.Close()
		c.conn = nil
		return fmt.Errorf("QGA sync handshake failed: %w", syncErr)
	}

	return nil
}

// reconnect attempts to re-establish the QGA connection.
func (c *QGAClient) reconnect() error {
	return c.connect()
}

// sync performs the guest-sync-delimited handshake.
// Sends 0xFF sentinel + JSON request, reads 0xFF sentinel + JSON response.
func (c *QGAClient) sync() error {
	syncID := rand.Int64N(1<<31 - 1)

	req := qgaRequest{
		Execute:   "guest-sync-delimited",
		Arguments: guestSyncArgs{ID: syncID},
	}

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal sync request: %w", err)
	}

	// Send 0xFF sentinel followed by JSON
	msg := append([]byte{0xFF}, data...)
	msg = append(msg, '\n')

	err = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err != nil {
		return fmt.Errorf("failed to set write deadline: %w", err)
	}

	_, err = c.conn.Write(msg)
	if err != nil {
		return fmt.Errorf("failed to write sync request: %w", err)
	}

	// Read response: skip bytes until we find 0xFF, then read JSON
	err = c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err != nil {
		return fmt.Errorf("failed to set read deadline: %w", err)
	}

	buf := make([]byte, 1)

	// Skip until 0xFF sentinel
	for {
		_, err := c.conn.Read(buf)
		if err != nil {
			return fmt.Errorf("failed to read sync response sentinel: %w", err)
		}

		if buf[0] == 0xFF {
			break
		}
	}

	// Read the JSON response line
	var respBuf []byte

	for {
		_, err := c.conn.Read(buf)
		if err != nil {
			return fmt.Errorf("failed to read sync response: %w", err)
		}

		if buf[0] == '\n' {
			break
		}

		respBuf = append(respBuf, buf[0])
	}

	var resp qgaResponse
	err = json.Unmarshal(respBuf, &resp)
	if err != nil {
		return fmt.Errorf("failed to unmarshal sync response: %w", err)
	}

	if resp.Error != nil {
		return fmt.Errorf("QGA sync error: %s: %s", resp.Error.Class, resp.Error.Desc)
	}

	var returnedID int64
	err = json.Unmarshal(resp.Return, &returnedID)
	if err != nil {
		return fmt.Errorf("failed to unmarshal sync ID: %w", err)
	}

	if returnedID != syncID {
		return fmt.Errorf("QGA sync ID mismatch: sent %d, got %d", syncID, returnedID)
	}

	return nil
}

// runOnce sends a QGA command and returns the raw JSON response (no retry).
func (c *QGAClient) runOnce(req qgaRequest) (json.RawMessage, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	data = append(data, '\n')

	err = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err != nil {
		return nil, fmt.Errorf("failed to set write deadline: %w", err)
	}

	_, err = c.conn.Write(data)
	if err != nil {
		return nil, fmt.Errorf("failed to write request: %w", err)
	}

	err = c.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	if err != nil {
		return nil, fmt.Errorf("failed to set read deadline: %w", err)
	}

	// Read JSON response line (QGA responses are newline-delimited)
	// Skip any 0xFF sentinels that might precede the response
	var respBuf []byte

	buf := make([]byte, 1)

	for {
		_, err := c.conn.Read(buf)
		if err != nil {
			return nil, fmt.Errorf("failed to read response: %w", err)
		}

		if buf[0] == 0xFF {
			continue // skip sentinel bytes
		}

		if buf[0] == '\n' {
			if len(respBuf) > 0 {
				break
			}

			continue // skip empty lines
		}

		respBuf = append(respBuf, buf[0])
	}

	var resp qgaResponse
	err = json.Unmarshal(respBuf, &resp)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("QGA error: %s: %s", resp.Error.Class, resp.Error.Desc)
	}

	return resp.Return, nil
}

// run sends a QGA command with auto-reconnection on failure.
func (c *QGAClient) run(req qgaRequest) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	result, err := c.runOnce(req)
	if err == nil {
		return result, nil
	}

	// Wait briefly before reconnecting — the socket may be recreated by guest agent restart
	time.Sleep(2 * time.Second)

	// Try reconnecting once and retry
	reconnErr := c.reconnect()
	if reconnErr != nil {
		return nil, fmt.Errorf("original error: %w; reconnect to %s/%s failed: %w", err, c.network, c.address, reconnErr)
	}

	return c.runOnce(req)
}

// Ping checks if the guest agent is responsive.
func (c *QGAClient) Ping() error {
	_, err := c.run(qgaRequest{Execute: "guest-ping"})
	return err
}

// Exec executes a command inside the guest via guest-exec.
// Returns the PID of the spawned process.
func (c *QGAClient) Exec(path string, args, env []string, stdinData []byte) (int, error) {
	execArgs := guestExecArgs{
		Path:          path,
		Arg:           args,
		Env:           env,
		CaptureOutput: true,
	}

	if len(stdinData) > 0 {
		execArgs.InputData = base64.StdEncoding.EncodeToString(stdinData)
	}

	raw, err := c.run(qgaRequest{
		Execute:   "guest-exec",
		Arguments: execArgs,
	})
	if err != nil {
		return 0, fmt.Errorf("guest-exec failed: %w", err)
	}

	var result guestExecResult
	unmarshalErr := json.Unmarshal(raw, &result)
	if unmarshalErr != nil {
		return 0, fmt.Errorf("failed to unmarshal exec result: %w", unmarshalErr)
	}

	return result.PID, nil
}

// ExecStatus queries the status of a previously started guest-exec process.
func (c *QGAClient) ExecStatus(pid int) (*GuestExecStatusResult, error) {
	raw, err := c.run(qgaRequest{
		Execute:   "guest-exec-status",
		Arguments: map[string]int{"pid": pid},
	})
	if err != nil {
		return nil, fmt.Errorf("guest-exec-status failed: %w", err)
	}

	var result GuestExecStatusResult
	unmarshalErr2 := json.Unmarshal(raw, &result)
	if unmarshalErr2 != nil {
		return nil, fmt.Errorf("failed to unmarshal exec status: %w", unmarshalErr2)
	}

	return &result, nil
}

// Close closes the QGA connection.
func (c *QGAClient) Close() error {
	if c.conn != nil {
		err := c.conn.Close()
		if err != nil {
			return fmt.Errorf("close: %w", err)
		}
	}

	return nil
}
