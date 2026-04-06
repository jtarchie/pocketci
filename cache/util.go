package cache

import (
	"fmt"
	"io"
)

// newPipe creates a pipe for streaming data.
func newPipe() (*io.PipeReader, *io.PipeWriter) {
	return io.Pipe()
}

// copyBuffer copies from src to dst using an internal buffer.
func copyBuffer(dst io.Writer, src io.Reader) (int64, error) {
	n, err := io.Copy(dst, src)
	if err != nil {
		return n, fmt.Errorf("copy: %w", err)
	}

	return n, nil
}
