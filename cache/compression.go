package cache

import (
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
)

// Compressor provides compression and decompression of data streams.
type Compressor interface {
	// Compress wraps a writer to compress data written to it.
	Compress(w io.Writer) (io.WriteCloser, error)

	// Decompress wraps a reader to decompress data read from it.
	Decompress(r io.Reader) (io.ReadCloser, error)

	// Extension returns the file extension for this compression type.
	Extension() string
}

// ZstdCompressor implements Compressor using zstandard compression.
type ZstdCompressor struct {
	level zstd.EncoderLevel
}

// NewZstdCompressor creates a new zstd compressor with the given level.
// Level 0 uses the default compression level.
func NewZstdCompressor(level int) *ZstdCompressor {
	encoderLevel := zstd.SpeedDefault
	if level > 0 {
		switch {
		case level <= 3:
			encoderLevel = zstd.SpeedFastest
		case level <= 6:
			encoderLevel = zstd.SpeedDefault
		case level <= 9:
			encoderLevel = zstd.SpeedBetterCompression
		default:
			encoderLevel = zstd.SpeedBestCompression
		}
	}

	return &ZstdCompressor{level: encoderLevel}
}

// Compress implements Compressor.
func (z *ZstdCompressor) Compress(w io.Writer) (io.WriteCloser, error) {
	encoder, err := zstd.NewWriter(w, zstd.WithEncoderLevel(z.level))
	if err != nil {
		return nil, fmt.Errorf("failed to create zstd encoder: %w", err)
	}

	return encoder, nil
}

// Decompress implements Compressor.
func (z *ZstdCompressor) Decompress(r io.Reader) (io.ReadCloser, error) {
	decoder, err := zstd.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("failed to create zstd decoder: %w", err)
	}

	return decoder.IOReadCloser(), nil
}

// Extension implements Compressor.
func (z *ZstdCompressor) Extension() string {
	return ".zst"
}

// NoCompressor implements Compressor without any compression.
type NoCompressor struct{}

// Compress implements Compressor.
func (n *NoCompressor) Compress(w io.Writer) (io.WriteCloser, error) {
	return &nopWriteCloser{w}, nil
}

// Decompress implements Compressor.
func (n *NoCompressor) Decompress(r io.Reader) (io.ReadCloser, error) {
	return io.NopCloser(r), nil
}

// Extension implements Compressor.
func (n *NoCompressor) Extension() string {
	return ""
}

type nopWriteCloser struct {
	io.Writer
}

func (n *nopWriteCloser) Close() error {
	return nil
}

// NewCompressor creates a compressor based on the algorithm name.
func NewCompressor(algorithm string) Compressor {
	switch algorithm {
	case "zstd", "zstandard":
		return NewZstdCompressor(0)
	case "none", "":
		return &NoCompressor{}
	default:
		// Default to zstd if unknown
		return NewZstdCompressor(0)
	}
}
