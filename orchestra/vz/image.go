//go:build darwin && cgo

package vz

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"

	"github.com/kdomanski/iso9660"
)

const (
	ubuntuBaseURL = "https://cloud-images.ubuntu.com/noble/current"
)

// imageArch returns the Ubuntu cloud image arch suffix for the current platform.
func imageArch() string {
	switch runtime.GOARCH {
	case "arm64":
		return "arm64"
	default:
		return "amd64"
	}
}

// imageFilename returns the expected cloud image filename.
// Apple Virtualization framework needs raw disk images, so we download
// the .img file (which is qcow2 for Ubuntu) and convert it.
func imageFilename() string {
	return fmt.Sprintf("noble-server-cloudimg-%s.img", imageArch())
}

// rawImageFilename returns the filename for the converted raw disk image.
func rawImageFilename() string {
	return fmt.Sprintf("noble-server-cloudimg-%s.raw", imageArch())
}

// downloadImage downloads the Ubuntu Noble cloud image to cacheDir if not already cached.
// Returns the path to the cached base image in raw format.
func downloadImage(cacheDir string) (string, error) {
	rawPath := filepath.Join(cacheDir, rawImageFilename())

	// Check if already cached in raw format
	_, statErr := os.Stat(rawPath)
	if statErr == nil {
		return rawPath, nil
	}

	err := os.MkdirAll(cacheDir, 0o755)
	if err != nil {
		return "", fmt.Errorf("failed to create cache dir: %w", err)
	}

	filename := imageFilename()
	qcow2Path := filepath.Join(cacheDir, filename)

	// Download the qcow2 image if not cached
	_, qcow2StatErr := os.Stat(qcow2Path)
	if qcow2StatErr != nil {
		imageURL := fmt.Sprintf("%s/%s", ubuntuBaseURL, filename)

		tmpPath := qcow2Path + ".tmp"

		resp, err := http.Get(imageURL) //nolint:gosec,noctx
		if err != nil {
			return "", fmt.Errorf("failed to download image from %s: %w", imageURL, err)
		}
		defer resp.Body.Close() //nolint:errcheck

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("failed to download image: HTTP %d", resp.StatusCode)
		}

		out, err := os.Create(tmpPath)
		if err != nil {
			return "", fmt.Errorf("failed to create temp file: %w", err)
		}
		defer out.Close() //nolint:errcheck

		_, copyErr := io.Copy(out, resp.Body)
		if copyErr != nil {
			_ = os.Remove(tmpPath)
			return "", fmt.Errorf("failed to write image: %w", copyErr)
		}

		closeErr := out.Close()
		if closeErr != nil {
			_ = os.Remove(tmpPath)
			return "", fmt.Errorf("failed to close temp file: %w", closeErr)
		}

		renameErr := os.Rename(tmpPath, qcow2Path)
		if renameErr != nil {
			_ = os.Remove(tmpPath)
			return "", fmt.Errorf("failed to rename temp file: %w", renameErr)
		}
	}

	// Convert qcow2 to raw format for Apple Virtualization framework
	err = convertToRaw(qcow2Path, rawPath)
	if err != nil {
		return "", fmt.Errorf("failed to convert image to raw: %w", err)
	}

	return rawPath, nil
}

// convertToRaw converts a qcow2 image to raw format using qemu-img.
func convertToRaw(qcow2Path, rawPath string) error {
	tmpPath := rawPath + ".tmp"

	cmd := qemuImgCommand("convert", "-f", "qcow2", "-O", "raw", qcow2Path, tmpPath)

	output, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("qemu-img convert failed: %w: %s", err, string(output))
	}

	err = os.Rename(tmpPath, rawPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to rename raw image: %w", err)
	}

	return nil
}

// createDiskCopy creates a writable copy of the base image for use as the VM's root disk.
// Unlike QEMU's COW overlays, Apple VZ needs a full raw disk image.
func createDiskCopy(baseImage, diskPath string) error {
	src, err := os.Open(baseImage)
	if err != nil {
		return fmt.Errorf("failed to open base image: %w", err)
	}
	defer src.Close() //nolint:errcheck

	dst, err := os.Create(diskPath)
	if err != nil {
		return fmt.Errorf("failed to create disk copy: %w", err)
	}
	defer dst.Close() //nolint:errcheck

	_, copyErr := io.Copy(dst, src)
	if copyErr != nil {
		_ = os.Remove(diskPath)
		return fmt.Errorf("failed to copy image: %w", copyErr)
	}

	return nil
}

// createSeedISO creates a cloud-init NoCloud seed ISO at isoPath.
// The ISO contains meta-data and user-data for VM initialization,
// including the vsock agent installation and virtiofs volume mount setup.
func createSeedISO(isoPath, namespace string) error {
	instanceID := fmt.Sprintf("ci-%s-%x", namespace, sha256.Sum256([]byte(namespace)))[:32]

	metaData := fmt.Sprintf(`instance-id: %s
local-hostname: ci-worker
`, instanceID)

	// The user-data installs the vsock agent and starts it as a systemd service.
	// The agent binary is cross-compiled and placed in the seed ISO or downloaded.
	// For now, we install a minimal Go runtime and build the agent from source,
	// or download a pre-compiled binary from a well-known location.
	//
	// In practice, the agent binary should be embedded or uploaded via virtiofs.
	// For the initial implementation, we use a simple approach:
	// 1. The host writes the agent binary to the shared volumes directory
	// 2. The VM mounts virtiofs and runs the agent from there
	//
	// As a fallback, we install socat and use a shell-based agent.
	userData := `#cloud-config
password: ci
chpasswd:
  expire: false
ssh_pwauth: true
package_update: true
packages:
  - socat
write_files:
  - path: /usr/local/bin/ci-agent.sh
    permissions: '0755'
    content: |
      #!/bin/bash
      # Minimal shell-based vsock agent
      # Reads JSON requests from stdin, executes commands, writes JSON responses to stdout
      # This is a fallback — the real agent is the compiled Go binary
      set -e
      
      handle_request() {
        local req="$1"
        local type=$(echo "$req" | python3 -c "import sys,json; print(json.load(sys.stdin)['type'])" 2>/dev/null)
        
        case "$type" in
          ping)
            echo '{"ok":true}'
            ;;
          exec)
            local path=$(echo "$req" | python3 -c "import sys,json; print(json.load(sys.stdin).get('path',''))")
            local args=$(echo "$req" | python3 -c "import sys,json; print(' '.join(json.load(sys.stdin).get('args',[])))")
            local env_vars=$(echo "$req" | python3 -c "import sys,json; [print(e) for e in json.load(sys.stdin).get('env',[])]")
            local stdin_data=$(echo "$req" | python3 -c "import sys,json,base64; d=json.load(sys.stdin).get('stdin_data',''); print(base64.b64decode(d).decode() if d else '')")
            
            # Create temp files for output capture
            local stdout_file=$(mktemp)
            local stderr_file=$(mktemp)
            local pid_file=$(mktemp)
            local exit_file=$(mktemp)
            
            (
              # Set environment
              while IFS= read -r line; do
                [ -n "$line" ] && export "$line"
              done <<< "$env_vars"
              
              if [ -n "$stdin_data" ]; then
                echo "$stdin_data" | $path $args > "$stdout_file" 2> "$stderr_file"
              else
                $path $args > "$stdout_file" 2> "$stderr_file"
              fi
              echo $? > "$exit_file"
            ) &
            local bgpid=$!
            echo "$bgpid:$stdout_file:$stderr_file:$exit_file" >> /tmp/ci-agent-procs
            echo "{\"ok\":true,\"pid\":$bgpid}"
            ;;
          exec-status)
            local pid=$(echo "$req" | python3 -c "import sys,json; print(json.load(sys.stdin)['pid'])")
            local proc_info=$(grep "^$pid:" /tmp/ci-agent-procs 2>/dev/null | tail -1)
            
            if [ -z "$proc_info" ]; then
              echo "{\"ok\":false,\"error\":\"unknown PID: $pid\"}"
              return
            fi
            
            local stdout_file=$(echo "$proc_info" | cut -d: -f2)
            local stderr_file=$(echo "$proc_info" | cut -d: -f3)
            local exit_file=$(echo "$proc_info" | cut -d: -f4)
            
            if kill -0 "$pid" 2>/dev/null; then
              echo '{"ok":true,"exited":false}'
            else
              local exit_code=$(cat "$exit_file" 2>/dev/null || echo "1")
              local stdout_b64=$(base64 -w0 "$stdout_file" 2>/dev/null || echo "")
              local stderr_b64=$(base64 -w0 "$stderr_file" 2>/dev/null || echo "")
              echo "{\"ok\":true,\"exited\":true,\"exit_code\":$exit_code,\"stdout\":\"$stdout_b64\",\"stderr\":\"$stderr_b64\"}"
            fi
            ;;
          *)
            echo "{\"ok\":false,\"error\":\"unknown type: $type\"}"
            ;;
        esac
      }
      
      # Listen on vsock — this requires socat with vsock support
      # or the compiled Go agent
      touch /tmp/ci-agent-procs
      while IFS= read -r line; do
        handle_request "$line"
      done

  - path: /etc/systemd/system/ci-agent.service
    content: |
      [Unit]
      Description=CI Vsock Agent
      After=network.target
      
      [Service]
      Type=simple
      # Try Go agent first, fall back to shell agent
      ExecStart=/bin/bash -c 'if [ -x /mnt/volumes/.ci-agent ]; then exec /mnt/volumes/.ci-agent; elif [ -x /usr/local/bin/ci-agent ]; then exec /usr/local/bin/ci-agent; else echo "No agent binary found, waiting..." && sleep infinity; fi'
      Restart=always
      RestartSec=2
      
      [Install]
      WantedBy=multi-user.target
runcmd:
  - mkdir -p /mnt/volumes
  - |
    # Try to mount virtiofs for shared volumes
    mount -t virtiofs volumes /mnt/volumes 2>/dev/null || true
  - |
    # If the agent binary is available on the shared volume, copy it
    if [ -f /mnt/volumes/.ci-agent ]; then
      cp /mnt/volumes/.ci-agent /usr/local/bin/ci-agent
      chmod +x /usr/local/bin/ci-agent
    fi
  - systemctl daemon-reload
  - systemctl enable --now ci-agent.service
`

	writer, err := iso9660.NewWriter()
	if err != nil {
		return fmt.Errorf("failed to create ISO writer: %w", err)
	}
	defer writer.Cleanup() //nolint:errcheck

	err = writer.AddFile(stringReader(metaData), "meta-data")
	if err != nil {
		return fmt.Errorf("failed to add meta-data: %w", err)
	}

	err = writer.AddFile(stringReader(userData), "user-data")
	if err != nil {
		return fmt.Errorf("failed to add user-data: %w", err)
	}

	f, err := os.Create(isoPath)
	if err != nil {
		return fmt.Errorf("failed to create ISO file: %w", err)
	}
	defer f.Close() //nolint:errcheck

	err = writer.WriteTo(f, "CIDATA")
	if err != nil {
		return fmt.Errorf("failed to write ISO: %w", err)
	}

	return nil
}

type stringReaderCloser struct {
	io.Reader
}

func (s *stringReaderCloser) Close() error { return nil }

func stringReader(s string) io.ReadCloser {
	return &stringReaderCloser{Reader: io.LimitReader(
		readerFunc(func(p []byte) (int, error) {
			return copy(p, s), io.EOF
		}),
		int64(len(s)),
	)}
}

type readerFunc func(p []byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) {
	return f(p)
}

// qemuImgCommand creates an exec.Cmd for qemu-img (used for image conversion).
func qemuImgCommand(args ...string) *qemuImgCmd {
	return &qemuImgCmd{args: args}
}

type qemuImgCmd struct {
	args []string
}

func (c *qemuImgCmd) CombinedOutput() ([]byte, error) {
	cmd := execCommand("qemu-img", c.args...)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("combined output: %w", err)
	}

	return out, nil
}
