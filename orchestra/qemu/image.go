package qemu

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
func imageFilename() string {
	return fmt.Sprintf("noble-server-cloudimg-%s.img", imageArch())
}

// downloadImage downloads the Ubuntu Noble cloud image to cacheDir if not already cached.
// Returns the path to the cached base image.
func downloadImage(cacheDir string) (string, error) {
	filename := imageFilename()
	cachePath := filepath.Join(cacheDir, filename)

	// Check if already cached
	_, statErr := os.Stat(cachePath)
	if statErr == nil {
		return cachePath, nil
	}

	err := os.MkdirAll(cacheDir, 0o755)
	if err != nil {
		return "", fmt.Errorf("failed to create cache dir: %w", err)
	}

	imageURL := fmt.Sprintf("%s/%s", ubuntuBaseURL, filename)

	// Download to a temp file then rename for atomicity
	tmpPath := cachePath + ".tmp"

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

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		_ = os.Remove(tmpPath)

		return "", fmt.Errorf("failed to write image: %w", err)
	}

	err = out.Close()
	if err != nil {
		_ = os.Remove(tmpPath)

		return "", fmt.Errorf("failed to close temp file: %w", err)
	}

	err = os.Rename(tmpPath, cachePath)
	if err != nil {
		_ = os.Remove(tmpPath)

		return "", fmt.Errorf("failed to rename temp file: %w", err)
	}

	return cachePath, nil
}

// createOverlay creates a qcow2 overlay backed by baseImage using qemu-img.
func createOverlay(baseImage, overlayPath string) error {
	// We use exec.Command here because qemu-img is a QEMU tool, not a guest command.
	// This is infrastructure setup, like the QEMU process itself.
	cmd := qemuImgCommand("create", "-f", "qcow2", "-b", baseImage, "-F", "qcow2", overlayPath)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("qemu-img create failed: %w: %s", err, string(output))
	}

	return nil
}

// createSeedISO creates a cloud-init NoCloud seed ISO at isoPath.
// The ISO contains meta-data and user-data for VM initialization,
// including QGA installation and 9p volume mount setup.
func createSeedISO(isoPath, namespace string) error {
	instanceID := fmt.Sprintf("ci-%s-%x", namespace, sha256.Sum256([]byte(namespace)))[:32]

	metaData := fmt.Sprintf(`instance-id: %s
local-hostname: ci-worker
`, instanceID)

	userData := `#cloud-config
password: ci
chpasswd:
  expire: false
ssh_pwauth: true
package_update: true
packages:
  - qemu-guest-agent
runcmd:
  - systemctl enable --now qemu-guest-agent
  - mkdir -p /mnt/volumes
  - |
    if ! mountpoint -q /mnt/volumes; then
      mount -t 9p -o trans=virtio,version=9p2000.L,msize=104857600 volumes /mnt/volumes || true
    fi
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
