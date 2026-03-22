package native

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/jtarchie/pocketci/orchestra"
)

// NativeSandbox runs each Exec call as a fresh OS process in a persistent
// working directory. No idle process is needed — native is direct OS execution.
type NativeSandbox struct {
	dir     string
	baseEnv []string
	n       *Native
}

var _ orchestra.Sandbox = (*NativeSandbox)(nil)

// ID returns the sandbox working directory path as its identifier.
func (s *NativeSandbox) ID() string {
	return s.dir
}

// Exec runs cmd as a fresh OS process inside the sandbox directory.
// env merges with (and overrides) the sandbox base env.
// workDir is relative to the sandbox dir; if empty, the sandbox dir is used.
func (s *NativeSandbox) Exec(
	ctx context.Context,
	cmd []string,
	env map[string]string,
	workDir string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) (orchestra.ContainerStatus, error) {
	if len(cmd) == 0 {
		return nil, errors.New("sandbox exec: command must not be empty")
	}

	mergedEnv := make([]string, len(s.baseEnv), len(s.baseEnv)+len(env))
	copy(mergedEnv, s.baseEnv)

	for k, v := range env {
		mergedEnv = append(mergedEnv, k+"="+v)
	}

	dir := s.dir
	if workDir != "" {
		if filepath.IsAbs(workDir) {
			dir = workDir
		} else {
			dir = filepath.Join(s.dir, workDir)
		}
	}

	//nolint:gosec
	command := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	command.Dir = dir
	command.Env = mergedEnv
	command.Stdout = stdout
	command.Stderr = stderr

	if stdin != nil {
		command.Stdin = stdin
	}

	err := command.Run()

	exitCode := 0

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("sandbox exec: failed to run command: %w", err)
		}
	}

	return &Status{
		exitCode: exitCode,
		isDone:   true,
	}, nil
}

// Cleanup removes the sandbox working directory.
func (s *NativeSandbox) Cleanup(_ context.Context) error {
	err := os.RemoveAll(s.dir)
	if err != nil {
		return fmt.Errorf("sandbox cleanup: failed to remove dir: %w", err)
	}

	return nil
}

// StartSandbox implements orchestra.SandboxDriver.
// It creates a temporary working directory and symlinks any requested mounts.
func (n *Native) StartSandbox(ctx context.Context, task orchestra.Task) (orchestra.Sandbox, error) {
	containerName := fmt.Sprintf("%x", sha256.Sum256(fmt.Appendf(nil, "%s-%s-sandbox", n.namespace, task.ID)))

	dir, err := os.MkdirTemp(n.path, containerName)
	if err != nil {
		return nil, fmt.Errorf("sandbox: failed to create dir: %w", err)
	}

	for _, m := range task.Mounts {
		vol, err := n.CreateVolume(ctx, m.Name, 0)
		if err != nil {
			_ = os.RemoveAll(dir)

			return nil, fmt.Errorf("sandbox: failed to create volume: %w", err)
		}

		nativeVol, _ := vol.(*Volume)

		err = os.Symlink(nativeVol.path, filepath.Join(dir, m.Path))
		if err != nil {
			_ = os.RemoveAll(dir)

			return nil, fmt.Errorf("sandbox: failed to symlink volume: %w", err)
		}
	}

	baseEnv := []string{}
	for k, v := range task.Env {
		baseEnv = append(baseEnv, k+"="+v)
	}

	return &NativeSandbox{
		dir:     dir,
		baseEnv: baseEnv,
		n:       n,
	}, nil
}
