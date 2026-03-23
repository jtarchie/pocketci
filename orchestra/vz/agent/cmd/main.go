//go:build linux

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os/exec"
	"sync"
	"sync/atomic"

	"github.com/mdlayher/vsock"
)

const vsockPort = 1024

type request struct {
	Type      string   `json:"type"`
	Path      string   `json:"path,omitempty"`
	Args      []string `json:"args,omitempty"`
	Env       []string `json:"env,omitempty"`
	StdinData string   `json:"stdin_data,omitempty"`
	PID       int      `json:"pid,omitempty"`
}

type response struct {
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	PID      int    `json:"pid,omitempty"`
	Exited   bool   `json:"exited,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
}

type processResult struct {
	mu       sync.Mutex
	exited   bool
	exitCode int
	stdout   []byte
	stderr   []byte
}

var (
	nextPID   atomic.Int64
	processes sync.Map
)

func main() {
	log.SetPrefix("ci-agent: ")
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	listener, err := vsock.Listen(vsockPort, nil)
	if err != nil {
		log.Fatalf("failed to listen on vsock port %d: %v", vsockPort, err)
	}
	defer listener.Close() //nolint:errcheck

	log.Printf("listening on vsock port %d", vsockPort)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("accept error: %v", err)

			continue
		}

		go handleConnection(conn)
	}
}

func handleConnection(conn net.Conn) {
	defer conn.Close() //nolint:errcheck

	decoder := json.NewDecoder(conn)
	encoder := json.NewEncoder(conn)

	for {
		var req request
		if err := decoder.Decode(&req); err != nil {
			if err == io.EOF {
				return
			}

			log.Printf("decode error: %v", err)

			return
		}

		var resp response

		switch req.Type {
		case "ping":
			resp = response{OK: true}
		case "exec":
			resp = handleExec(req)
		case "exec-status":
			resp = handleExecStatus(req)
		default:
			resp = response{OK: false, Error: "unknown request type: " + req.Type}
		}

		if err := encoder.Encode(resp); err != nil {
			log.Printf("encode error: %v", err)

			return
		}
	}
}

func handleExec(req request) response {
	cmd := exec.Command(req.Path, req.Args...) //nolint:gosec
	cmd.Env = req.Env

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	if req.StdinData != "" {
		decoded, err := base64.StdEncoding.DecodeString(req.StdinData)
		if err != nil {
			return response{OK: false, Error: fmt.Sprintf("failed to decode stdin: %v", err)}
		}

		cmd.Stdin = bytes.NewReader(decoded)
	}

	pid := int(nextPID.Add(1))
	proc := &processResult{}
	processes.Store(pid, proc)

	if err := cmd.Start(); err != nil {
		proc.mu.Lock()
		proc.exited = true
		proc.exitCode = 1
		proc.stderr = []byte(err.Error())
		proc.mu.Unlock()

		return response{OK: true, PID: pid}
	}

	go func() {
		exitCode := 0

		if err := cmd.Wait(); err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = 1
			}
		}

		proc.mu.Lock()
		proc.exited = true
		proc.exitCode = exitCode
		proc.stdout = stdoutBuf.Bytes()
		proc.stderr = stderrBuf.Bytes()
		proc.mu.Unlock()
	}()

	return response{OK: true, PID: pid}
}

func handleExecStatus(req request) response {
	val, ok := processes.Load(req.PID)
	if !ok {
		return response{OK: false, Error: fmt.Sprintf("unknown PID: %d", req.PID)}
	}

	proc := val.(*processResult)
	proc.mu.Lock()
	defer proc.mu.Unlock()

	resp := response{
		OK:       true,
		Exited:   proc.exited,
		ExitCode: proc.exitCode,
	}

	if proc.exited {
		if len(proc.stdout) > 0 {
			resp.Stdout = base64.StdEncoding.EncodeToString(proc.stdout)
		}

		if len(proc.stderr) > 0 {
			resp.Stderr = base64.StdEncoding.EncodeToString(proc.stderr)
		}
	}

	return resp
}
