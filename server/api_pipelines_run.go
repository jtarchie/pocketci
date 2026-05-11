package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/jtarchie/pocketci/storage"
	"github.com/klauspost/compress/zstd"
	"github.com/labstack/echo/v5"
)

// DefaultMaxWorkdirBytes is the fallback cap when the server is configured
// with 0 (or nothing wired through). Rejects zstd bombs that would expand
// past the cap. Echo's BodyLimit caps the compressed body; this caps the
// decompressed stream, which a bomb can inflate by 1,000,000× or more.
// The caller (server config, CLI flag CI_MAX_WORKDIR_MB) overrides this.
const DefaultMaxWorkdirBytes int64 = 1 << 30 // 1 GiB

// ErrWorkdirTooLarge is returned when the decompressed workdir stream
// exceeds the configured cap.
var ErrWorkdirTooLarge = errors.New("workdir decompressed size exceeds cap")

// cappedReadCloser bounds the total bytes read through it. Reads past the
// cap return ErrWorkdirTooLarge so callers (tar extractors, volume copiers)
// surface a clear error rather than silently truncating.
type cappedReadCloser struct {
	inner io.ReadCloser
	cap   int64
	read  int64
}

func (c *cappedReadCloser) Read(p []byte) (int, error) {
	remaining := c.cap - c.read
	if remaining <= 0 {
		return 0, ErrWorkdirTooLarge
	}

	if int64(len(p)) > remaining {
		p = p[:remaining]
	}

	n, err := c.inner.Read(p)
	c.read += int64(n)

	if c.read >= c.cap && (err == nil || errors.Is(err, io.EOF)) {
		// Peek one extra byte to distinguish "exactly at cap" from
		// "cap reached with more data available".
		var peek [1]byte
		pn, _ := c.inner.Read(peek[:])

		if pn > 0 {
			return n, ErrWorkdirTooLarge
		}
	}

	switch {
	case err == nil, errors.Is(err, io.EOF):
		return n, err //nolint:wrapcheck // io.EOF is a sentinel and must pass through unwrapped
	default:
		return n, fmt.Errorf("cappedRead: %w", err)
	}
}

func (c *cappedReadCloser) Close() error {
	err := c.inner.Close()
	if err != nil {
		return fmt.Errorf("cappedClose: %w", err)
	}

	return nil
}

// zstdReadCloser wraps *zstd.Decoder to satisfy io.ReadCloser.
// zstd.Decoder.Close() has no return value, so we adapt it here.
type zstdReadCloser struct{ *zstd.Decoder }

func (z zstdReadCloser) Close() error { z.Decoder.Close(); return nil }

// parseRunInput extracts args and an optional workdir tar from the request,
// trying multipart streaming first then falling back to JSON body.
//
// maxWorkdirBytes bounds the decompressed "workdir" zstd stream to reject
// zip-bomb uploads. Zero or negative values fall back to
// DefaultMaxWorkdirBytes so unwired callers still get the cap.
//
// Part ordering contract: clients must send "args" before "workdir".
// Multipart is a single stream — once we hand the zstd-wrapped "workdir"
// part to the caller, we cannot advance to further parts without
// invalidating the wrapped reader. Parts encountered after "workdir" are
// silently ignored; the CLI client already honours this ordering.
// Non-workdir parts are closed eagerly so the connection reader can advance.
func parseRunInput(ctx *echo.Context, maxWorkdirBytes int64) ([]string, io.ReadCloser) {
	if maxWorkdirBytes <= 0 {
		maxWorkdirBytes = DefaultMaxWorkdirBytes
	}

	var args []string
	var workdirTar io.ReadCloser

	mr, mrErr := ctx.Request().MultipartReader()
	if mrErr == nil {
		for {
			part, partErr := mr.NextPart()
			if partErr == io.EOF {
				break
			}
			if partErr != nil {
				break
			}

			switch part.FormName() {
			case "args":
				data, _ := io.ReadAll(part)
				_ = json.Unmarshal(data, &args)
				_ = part.Close()
			case "workdir":
				// The returned zstd reader wraps `part`; the caller
				// defer-closes workdirTar, which transitively finishes
				// the part. Return immediately — see ordering contract.
				zr, zErr := zstd.NewReader(part)
				if zErr != nil {
					_ = part.Close()

					continue
				}
				// Cap the decompressed stream to reject zstd bombs.
				workdirTar = &cappedReadCloser{
					inner: zstdReadCloser{zr},
					cap:   maxWorkdirBytes,
				}
			default:
				_ = part.Close()
			}

			if workdirTar != nil {
				break
			}
		}
	} else {
		var req struct {
			Args []string `json:"args"`
		}
		_ = json.NewDecoder(ctx.Request().Body).Decode(&req)
		args = req.Args
	}

	return args, workdirTar
}

// Run handles POST /api/pipelines/:name/run - Run a stored pipeline by name (synchronous SSE stream).
func (c *APIPipelinesController) Run(ctx *echo.Context) error {
	name := ctx.Param("name")

	args, workdirTar := parseRunInput(ctx, c.maxWorkdirBytes)
	if workdirTar != nil {
		defer func() {
			err := workdirTar.Close()
			if err != nil {
				c.logger.Warn("workdir.tar.close", slog.String("error", err.Error()))
			}
		}()
	}

	w := ctx.Response()

	// Check pipeline-level RBAC before executing.
	pipeline, err := c.store.GetPipelineByName(ctx.Request().Context(), name)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			nfJsonErr := ctx.JSON(http.StatusNotFound, map[string]string{
				"error": "pipeline not found",
			})
			if nfJsonErr != nil {
				return fmt.Errorf("run not found response: %w", nfJsonErr)
			}

			return nil
		}

		geJsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "internal server error",
		})
		if geJsonErr != nil {
			return fmt.Errorf("run get error response: %w", geJsonErr)
		}

		return nil
	}

	runRbacErr := checkPipelineRBAC(ctx, pipeline)
	if runRbacErr != nil {
		return nil //nolint:nilerr // helper already wrote the HTTP response
	}

	if pipeline.Paused {
		pausedJsonErr := ctx.JSON(http.StatusConflict, map[string]string{
			"error": "pipeline is paused",
		})
		if pausedJsonErr != nil {
			return fmt.Errorf("run paused response: %w", pausedJsonErr)
		}

		return nil
	}

	err = c.execService.RunByNameSync(ctx.Request().Context(), name, args, workdirTar, w)
	if err != nil {
		return c.runHandleSyncError(ctx, w, err)
	}

	return nil
}

// runHandleSyncError handles an error from RunByNameSync. If the response has
// not yet been committed it writes an appropriate HTTP error; otherwise it
// appends an SSE error event to the already-started stream.
func (c *APIPipelinesController) runHandleSyncError(ctx *echo.Context, w http.ResponseWriter, runErr error) error {
	echoResp, _ := echo.UnwrapResponse(ctx.Response())
	if echoResp != nil && echoResp.Committed {
		errData, _ := json.Marshal(map[string]string{"event": "error", "message": runErr.Error()})
		fmt.Fprintf(w, "data: %s\n\n", errData) //nolint:errcheck
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}

		return nil
	}

	if errors.Is(runErr, storage.ErrNotFound) {
		syncNFJsonErr := ctx.JSON(http.StatusNotFound, map[string]string{
			"error": "pipeline not found",
		})
		if syncNFJsonErr != nil {
			return fmt.Errorf("run sync not found response: %w", syncNFJsonErr)
		}

		return nil
	}

	syncErrJson := ctx.JSON(http.StatusInternalServerError, map[string]string{
		"error": runErr.Error(),
	})
	if syncErrJson != nil {
		return fmt.Errorf("run sync error response: %w", syncErrJson)
	}

	return nil
}
