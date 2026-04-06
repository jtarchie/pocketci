package native

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jtarchie/pocketci/cache"
)

// CopyToVolume implements cache.VolumeDataAccessor.
// Extracts a tar archive to the volume directory.
func (n *Native) CopyToVolume(_ context.Context, volumeName string, reader io.Reader) error {
	volumePath := filepath.Join(n.path, volumeName)

	// Ensure volume directory exists
	err := os.MkdirAll(volumePath, os.ModePerm)
	if err != nil {
		return fmt.Errorf("failed to create volume directory: %w", err)
	}

	tr := tar.NewReader(reader)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return fmt.Errorf("failed to read tar header: %w", err)
		}

		// Security: prevent path traversal
		target := filepath.Join(volumePath, header.Name)
		if !strings.HasPrefix(target, volumePath) {
			return fmt.Errorf("invalid tar path: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			err := os.MkdirAll(target, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("failed to create directory: %w", err)
			}
		case tar.TypeReg:
			// Ensure parent directory exists
			err := os.MkdirAll(filepath.Dir(target), os.ModePerm)
			if err != nil {
				return fmt.Errorf("failed to create parent directory: %w", err)
			}

			file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("failed to create file: %w", err)
			}

			_, copyErr := io.Copy(file, tr)
			_ = file.Close()

			if copyErr != nil {
				return fmt.Errorf("failed to write file: %w", copyErr)
			}
		case tar.TypeSymlink:
			// Ensure parent directory exists
			err := os.MkdirAll(filepath.Dir(target), os.ModePerm)
			if err != nil {
				return fmt.Errorf("failed to create parent directory: %w", err)
			}

			err = os.Symlink(header.Linkname, target)
			if err != nil {
				return fmt.Errorf("failed to create symlink: %w", err)
			}
		}
	}

	return nil
}

// CopyFromVolume implements cache.VolumeDataAccessor.
// Creates a tar archive of the volume directory contents.
func (n *Native) CopyFromVolume(_ context.Context, volumeName string) (io.ReadCloser, error) {
	volumePath := filepath.Join(n.path, volumeName)

	// Check if volume exists
	_, statErr := os.Stat(volumePath)
	if os.IsNotExist(statErr) {
		return nil, fmt.Errorf("volume directory does not exist: %s", volumePath)
	}

	pr, pw := io.Pipe()

	go func() {
		tw := tar.NewWriter(pw)

		err := filepath.Walk(volumePath, tarWalkFunc(volumePath, tw))
		if err != nil {
			_ = tw.Close()
			pw.CloseWithError(err)

			return
		}

		closeErr := tw.Close()
		if closeErr != nil {
			pw.CloseWithError(closeErr)

			return
		}

		_ = pw.Close()
	}()

	return pr, nil
}

// ReadFilesFromVolume implements cache.VolumeDataAccessor.
// Creates a tar archive containing only the requested paths from the volume.
func (n *Native) ReadFilesFromVolume(_ context.Context, volumeName string, filePaths ...string) (io.ReadCloser, error) {
	volumePath := filepath.Join(n.path, volumeName)

	_, statErr := os.Stat(volumePath)
	if os.IsNotExist(statErr) {
		return nil, fmt.Errorf("volume directory does not exist: %s", volumePath)
	}

	pr, pw := io.Pipe()

	go func() {
		tw := tar.NewWriter(pw)

		var walkErr error

		for _, fp := range filePaths {
			walkErr = tarPath(tw, volumePath, fp)
			if walkErr != nil {
				break
			}
		}

		if walkErr != nil {
			_ = tw.Close()
			pw.CloseWithError(walkErr)

			return
		}

		err := tw.Close()
		if err != nil {
			pw.CloseWithError(err)

			return
		}

		_ = pw.Close()
	}()

	return pr, nil
}

// tarFileEntry writes a single file entry to the tar writer.
func tarFileEntry(tw *tar.Writer, name, target string, info os.FileInfo) error {
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return fmt.Errorf("failed to create tar header for %s: %w", name, err)
	}

	header.Name = name

	if info.Mode()&os.ModeSymlink != 0 {
		linkTarget, err := os.Readlink(target)
		if err != nil {
			return fmt.Errorf("failed to read symlink: %w", err)
		}

		header.Linkname = linkTarget
	}

	err = tw.WriteHeader(header)
	if err != nil {
		return fmt.Errorf("failed to write tar header: %w", err)
	}

	if !info.Mode().IsRegular() {
		return nil
	}

	file, err := os.Open(target)
	if err != nil {
		return fmt.Errorf("failed to open file %s: %w", name, err)
	}

	defer func() { _ = file.Close() }()

	_, err = io.Copy(tw, file)
	if err != nil {
		return fmt.Errorf("failed to write file to tar: %w", err)
	}

	return nil
}

// tarPath writes a single path (file or directory) to the tar writer.
func tarPath(tw *tar.Writer, volumePath, fp string) error {
	target := filepath.Join(volumePath, fp)

	// Security: prevent path traversal
	if !strings.HasPrefix(target, volumePath) {
		return fmt.Errorf("invalid path: %s", fp)
	}

	info, err := os.Lstat(target)
	if err != nil {
		return fmt.Errorf("failed to stat %s: %w", fp, err)
	}

	if info.IsDir() {
		err = filepath.Walk(target, tarWalkFunc(volumePath, tw))
		if err != nil {
			return fmt.Errorf("walk: %w", err)
		}

		return nil
	}

	return tarFileEntry(tw, fp, target, info)
}

var _ cache.VolumeDataAccessor = (*Native)(nil)

// tarWalkFunc returns a filepath.WalkFunc that writes each entry to the tar writer
// with paths relative to volumePath.
func tarWalkFunc(volumePath string, tw *tar.Writer) filepath.WalkFunc {
	return func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(volumePath, path)
		if err != nil {
			return fmt.Errorf("failed to get relative path: %w", err)
		}

		if relPath == "." {
			return nil
		}

		return tarFileEntry(tw, relPath, path, fi)
	}
}
