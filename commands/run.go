package commands

import (
	"archive/tar"
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/jtarchie/pocketci/client"
	"github.com/klauspost/compress/zstd"
	"github.com/schollz/progressbar/v3"
)

// Run is the `ci run` command. It triggers a stored pipeline by name on a
// remote CI server and streams the result back. All execution, secrets, and
// driver configuration remain server-side.
type Run struct {
	ServerConfig
	Name      string        `arg:""                                              help:"Pipeline name to execute"`
	Args      []string      `arg:""                                              help:"Arguments passed to the pipeline via pipelineContext.args"          optional:"" passthrough:""`
	Timeout   time.Duration `env:"CI_TIMEOUT"                                    help:"Client-side timeout for the full execution (0 = no timeout)"`
	NoWorkdir bool          `help:"Skip uploading the current working directory"`
	Ignore    []string      `default:".git/**/*"                                 help:"Glob patterns to exclude from the workdir upload (comma-separated)" sep:","`
}

// sseEvent is parsed from a `data: {...}` SSE line.
type sseEvent struct {
	Event   string `json:"event"`
	Code    int    `json:"code"`
	RunID   string `json:"run_id"`
	Stream  string `json:"stream"`
	Data    string `json:"data"`
	Message string `json:"message"`
}

func (c *Run) Run(logger *slog.Logger) error {
	logger = logger.WithGroup("pipeline.run")

	tmpFile, contentType, err := c.buildMultipartBody(logger)
	if err != nil {
		return err
	}

	defer func() { _ = os.Remove(tmpFile.Name()) }()
	defer func() { _ = tmpFile.Close() }()

	bodySize, err := tmpFile.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("could not determine upload size: %w", err)
	}

	_, seekStartErr := tmpFile.Seek(0, io.SeekStart)
	if seekStartErr != nil {
		return fmt.Errorf("could not rewind temp file: %w", seekStartErr)
	}

	var opts []client.Option
	if c.Timeout > 0 {
		opts = append(opts, client.WithTimeout(c.Timeout))
	}

	apiClient := c.NewClient(opts...)

	logger.Info("pipeline.run.trigger", "name", c.Name, "url", RedactURL(apiClient.ServerURL()+"/api/pipelines/"+c.Name+"/run"), "args", c.Args, "upload_bytes", bodySize)

	bar := progressbar.NewOptions64(bodySize,
		progressbar.OptionSetDescription("uploading"),
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionShowBytes(true),
		progressbar.OptionSetVisibility(!c.NoWorkdir),
		progressbar.OptionOnCompletion(func() { fmt.Fprintln(os.Stderr) }),
	)

	resp, err := apiClient.RunPipeline(c.Name, io.TeeReader(tmpFile, bar), contentType, bodySize)
	if err != nil {
		return fmt.Errorf("run pipeline: %w", err)
	}
	defer func() { _ = resp.RawBody().Close() }()

	_ = bar.Finish()

	checkStatusErr := checkRunResponseStatus(resp.StatusCode(), resp.RawBody(), apiClient.ServerURL())
	if checkStatusErr != nil {
		return checkStatusErr
	}

	logger.Info("pipeline.run.streaming")

	return streamSSEEvents(resp.RawBody(), logger)
}

func checkRunResponseStatus(statusCode int, body io.Reader, serverURL string) error {
	switch statusCode {
	case 401:
		return &client.AuthRequiredError{ServerURL: serverURL}
	case 403:
		return &client.AccessDeniedError{ServerURL: serverURL}
	case 200:
		return nil
	default:
		b, _ := io.ReadAll(body)
		return fmt.Errorf("server returned %d: %s", statusCode, string(b))
	}
}

func (c *Run) buildMultipartBody(logger *slog.Logger) (*os.File, string, error) {
	tmpFile, err := os.CreateTemp("", "ci-upload-*.bin")
	if err != nil {
		return nil, "", fmt.Errorf("could not create temp file: %w", err)
	}

	mw := multipart.NewWriter(tmpFile)

	writeArgsErr := c.writeArgsField(mw)
	if writeArgsErr != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())

		return nil, "", writeArgsErr
	}

	if !c.NoWorkdir {
		err := c.writeWorkdirField(mw, logger)
		if err != nil {
			_ = tmpFile.Close()
			_ = os.Remove(tmpFile.Name())

			return nil, "", err
		}
	}

	mwCloseErr := mw.Close()
	if mwCloseErr != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())

		return nil, "", fmt.Errorf("could not close multipart writer: %w", mwCloseErr)
	}

	return tmpFile, mw.FormDataContentType(), nil
}

func (c *Run) writeArgsField(mw *multipart.Writer) error {
	argsData, err := json.Marshal(c.Args)
	if err != nil {
		return fmt.Errorf("could not encode args: %w", err)
	}

	fw, err := mw.CreateFormField("args")
	if err != nil {
		return fmt.Errorf("could not create args field: %w", err)
	}

	_, writeArgsDataErr := fw.Write(argsData)
	if writeArgsDataErr != nil {
		return fmt.Errorf("could not write args: %w", writeArgsDataErr)
	}

	return nil
}

func (c *Run) writeWorkdirField(mw *multipart.Writer, logger *slog.Logger) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("could not determine working directory: %w", err)
	}

	logger.Info("pipeline.run.workdir", "path", cwd, "ignore", c.Ignore)

	ff, err := mw.CreateFormFile("workdir", "workdir.tar.zst")
	if err != nil {
		return fmt.Errorf("could not create workdir part: %w", err)
	}

	zw, err := zstd.NewWriter(ff, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		return fmt.Errorf("could not create zstd writer: %w", err)
	}

	tarErr := tarDirectory(cwd, zw, c.Ignore)
	if tarErr != nil {
		return fmt.Errorf("could not tar working directory: %w", tarErr)
	}

	zwCloseErr := zw.Close()
	if zwCloseErr != nil {
		return fmt.Errorf("could not flush zstd stream: %w", zwCloseErr)
	}

	return nil
}

func streamSSEEvents(body io.Reader, logger *slog.Logger) error {
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		payload := strings.TrimPrefix(line, "data: ")

		var evt sseEvent
		err := json.Unmarshal([]byte(payload), &evt)
		if err != nil {
			logger.Debug("run.sse.unparseable", "line", payload)
			continue
		}

		switch evt.Event {
		case "exit":
			if evt.Message != "" {
				fmt.Fprintln(os.Stderr, evt.Message)
			}

			logger.Info("pipeline.run.exit", "code", evt.Code, "run_id", evt.RunID)
			os.Exit(evt.Code) //nolint:gocritic // intentional: propagate exit code
		case "error":
			fmt.Fprintln(os.Stderr, "error:", evt.Message)
			os.Exit(1) //nolint:gocritic
		case "":
			if evt.Stream == "stderr" {
				fmt.Fprint(os.Stderr, evt.Data)
			} else {
				fmt.Fprint(os.Stdout, evt.Data) //nolint:errcheck
			}
		}
	}

	err := scanner.Err()
	if err != nil {
		return fmt.Errorf("error reading stream: %w", err)
	}

	return nil
}

// tarDirectory writes a tar archive of dir to w, compressing with zstd.
// ignorePatterns is a list of doublestar glob patterns (relative to dir) to skip.
func tarDirectory(dir string, w io.Writer, ignorePatterns []string) error {
	tw := tar.NewWriter(w)
	defer func() { _ = tw.Close() }()

	walkErr := filepath.Walk(dir, tarWalkFunc(dir, tw, ignorePatterns))
	if walkErr != nil {
		return fmt.Errorf("walk directory: %w", walkErr)
	}

	return nil
}

// tarWalkFunc returns a filepath.WalkFunc that archives each file/dir into tw,
// skipping ignored paths and non-regular entries.
func tarWalkFunc(dir string, tw *tar.Writer, ignorePatterns []string) filepath.WalkFunc {
	return func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(dir, path)
		if err != nil {
			return fmt.Errorf("relative path: %w", err)
		}

		// Skip the root "." directory entry — extractors handle it implicitly.
		if relPath == "." {
			return nil
		}

		if len(ignorePatterns) > 0 && ignorePath(relPath, info.IsDir(), ignorePatterns) {
			if info.IsDir() {
				return filepath.SkipDir
			}

			return nil
		}

		// Only follow regular files and directories; skip symlinks, devices, etc.
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil
		}

		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return fmt.Errorf("could not build tar header for %q: %w", relPath, err)
		}

		hdr.Name = relPath
		// Produce portable headers: clear OS-specific fields that confuse
		// minimal tar implementations (e.g. busybox).
		hdr.Uid = 0
		hdr.Gid = 0
		hdr.Uname = ""
		hdr.Gname = ""
		hdr.AccessTime = time.Time{}
		hdr.ChangeTime = time.Time{}
		hdr.PAXRecords = nil // avoid PAX extensions
		hdr.Format = tar.FormatGNU

		writeHdrErr := tw.WriteHeader(hdr)
		if writeHdrErr != nil {
			return fmt.Errorf("could not write tar header for %q: %w", relPath, writeHdrErr)
		}

		if info.IsDir() {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("could not open %q: %w", path, err)
		}
		defer func() { _ = f.Close() }()

		_, copyErr := io.Copy(tw, f)
		if copyErr != nil {
			return fmt.Errorf("could not write %q to tar: %w", relPath, copyErr)
		}

		return nil
	}
}

// ignorePath returns true if relPath should be excluded based on the given glob patterns.
// For directories, it also probes whether anything inside the directory would match,
// enabling early SkipDir returns for patterns like ".git/**/*".
func ignorePath(relPath string, isDir bool, patterns []string) bool {
	// Normalise to forward slashes for doublestar.
	relPath = filepath.ToSlash(relPath)

	for _, pattern := range patterns {
		if ok, _ := doublestar.Match(pattern, relPath); ok {
			return true
		}

		// For directories, probe with a synthetic child path so that patterns
		// like ".git/**/*" cause the whole directory to be skipped.
		if isDir {
			if ok, _ := doublestar.Match(pattern, relPath+"/x"); ok {
				return true
			}
		}
	}

	return false
}
