package native

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/jtarchie/pocketci/orchestra"
)

type logBuffer struct {
	mu   sync.RWMutex
	data []byte
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.data = append(l.data, p...)

	return len(p), nil
}

func (l *logBuffer) Snapshot() string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return string(bytes.Clone(l.data))
}

// liveChunk carries a single chunk of output from a stream.
type liveChunk struct {
	data   []byte
	stream string // "stdout" or "stderr"
}

type Container struct {
	id        string
	command   *exec.Cmd
	stdoutLog *logBuffer
	stderrLog *logBuffer
	// liveCh delivers chunks in real time to a Logs(follow=true) caller.
	// Sends are non-blocking (chunks dropped from live view if full) so the
	// process is never stalled; logBuffers always have the full output.
	liveCh  chan liveChunk
	errChan chan error
}

// ID returns the container identifier (process-based, not persistent).
func (n *Container) ID() string {
	return n.id
}

func (n *Container) Cleanup(_ context.Context) error {
	return nil
}

// Logs retrieves container logs.
// When follow is false, returns a snapshot of all accumulated output so far.
// When follow is true, streams output in real time until the process exits or
// ctx is cancelled, then returns.
func (n *Container) Logs(ctx context.Context, stdout io.Writer, stderr io.Writer, follow bool) error {
	if !follow {
		_, err := io.WriteString(stdout, n.stdoutLog.Snapshot())
		if err != nil {
			return fmt.Errorf("failed to copy stdout: %w", err)
		}

		_, err = io.WriteString(stderr, n.stderrLog.Snapshot())
		if err != nil {
			return fmt.Errorf("failed to copy stderr: %w", err)
		}

		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case chunk, ok := <-n.liveCh:
			if !ok {
				// Channel closed: process has exited and all output is drained.
				return nil
			}

			var w io.Writer

			if chunk.stream == "stdout" {
				w = stdout
			} else {
				w = stderr
			}

			if _, err := w.Write(chunk.data); err != nil {
				return fmt.Errorf("failed to write %s: %w", chunk.stream, err)
			}
		}
	}
}

type Status struct {
	exitCode int
	isDone   bool
}

func (n *Status) ExitCode() int {
	return n.exitCode
}

func (n *Status) IsDone() bool {
	return n.isDone
}

func (n *Container) Status(ctx context.Context) (orchestra.ContainerStatus, error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("failed to get status: %w", context.Canceled)
	case err := <-n.errChan:
		if err != nil {
			var exitErr *exec.ExitError

			if !errors.As(err, &exitErr) {
				return nil, fmt.Errorf("failed to get status: %w", err)
			}
		}

		defer func() { n.errChan <- err }()

		return &Status{
			exitCode: n.command.ProcessState.ExitCode(),
			isDone:   n.command.ProcessState.Exited(),
		}, nil
	default:
		return &Status{
			exitCode: -1,
			isDone:   false,
		}, nil
	}
}

// teeStream reads from r, writes every chunk to log and sends a non-blocking
// copy on liveCh. Non-blocking sends ensure the process is never stalled if
// nobody is consuming liveCh; logBuffer always captures the full output.
func teeStream(stream string, r io.Reader, log *logBuffer, liveCh chan<- liveChunk) {
	buf := make([]byte, 4096)

	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			log.Write(chunk) //nolint:errcheck // logBuffer.Write never errors

			select {
			case liveCh <- liveChunk{data: chunk, stream: stream}:
			default:
				// liveCh full — live delivery skipped; logBuffer has the data.
			}
		}

		if err != nil {
			break
		}
	}
}

func (n *Native) RunContainer(ctx context.Context, task orchestra.Task) (orchestra.Container, error) {
	logger := n.logger.With("taskID", task.ID)

	containerName := fmt.Sprintf("%x", sha256.Sum256(fmt.Appendf(nil, "%s-%s", n.namespace, task.ID)))

	dir, err := os.MkdirTemp(n.path, containerName)
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	for _, mount := range task.Mounts {
		volume, err := n.CreateVolume(ctx, mount.Name, 0)
		if err != nil {
			logger.Error("volume.create.native.volume.error", "name", mount.Name, "err", err)

			return nil, fmt.Errorf("failed to create volume: %w", err)
		}

		nativeVolume, _ := volume.(*Volume)

		err = os.Symlink(nativeVolume.path, filepath.Join(dir, mount.Path))
		if err != nil {
			logger.Error("volume.create.native.symlink.error", "name", mount.Name, "err", err)

			return nil, fmt.Errorf("failed to create symlink: %w", err)
		}
	}

	errChan := make(chan error, 1)

	//nolint:gosec
	command := exec.CommandContext(ctx, task.Command[0], task.Command[1:]...)

	if task.WorkDir != "" {
		command.Dir = filepath.Join(dir, task.WorkDir)
	} else {
		command.Dir = dir
	}

	env := []string{}
	for k, v := range task.Env {
		env = append(env, k+"="+v)
	}

	command.Env = env

	stdoutLog := &logBuffer{}
	stderrLog := &logBuffer{}

	stdoutPipe, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderrPipe, err := command.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if task.Stdin != nil {
		command.Stdin = task.Stdin
	}

	if task.Image != "" {
		logger.Warn("orchestra.native.image.unsupported", "image", task.Image)
	}

	if task.User != "" {
		logger.Warn("orchestra.native.user.unsupported", "user", task.User)
	}

	if task.Privileged {
		logger.Warn("orchestra.native.privileged.unsupported", "msg", "privileged is not supported in native mode")
	}

	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("failed to start command: %w", err)
	}

	// liveCh is buffered so teeStream goroutines are never blocked by a slow
	// or absent Logs(follow=true) consumer.
	liveCh := make(chan liveChunk, 128)

	var streamWg sync.WaitGroup

	streamWg.Add(2)

	go func() {
		defer streamWg.Done()
		teeStream("stdout", stdoutPipe, stdoutLog, liveCh)
	}()

	go func() {
		defer streamWg.Done()
		teeStream("stderr", stderrPipe, stderrLog, liveCh)
	}()

	go func() {
		// Wait for the process then for both stream goroutines to drain their
		// pipes before closing liveCh, so Logs(follow=true) sees all output.
		err := command.Wait()
		streamWg.Wait()
		close(liveCh)

		if err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				logger.Error("orchestra.native.run.failed", "err", err)
				errChan <- fmt.Errorf("failed to run command: %w", err)

				return
			}
		}

		errChan <- nil
	}()

	return &Container{
		id:        task.ID,
		command:   command,
		stdoutLog: stdoutLog,
		stderrLog: stderrLog,
		liveCh:    liveCh,
		errChan:   errChan,
	}, nil
}
