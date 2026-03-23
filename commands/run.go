package commands

import (
	"archive/tar"
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/go-resty/resty/v2"
	"github.com/klauspost/compress/zstd"
	"github.com/schollz/progressbar/v3"
)

// Run is the `ci run` command. It triggers a stored pipeline by name on a
// remote CI server and streams the result back. All execution, secrets, and
// driver configuration remain server-side.
type Run struct {
	Name       string        `arg:""                                              help:"Pipeline name to execute"`
	Args       []string      `arg:""                                              help:"Arguments passed to the pipeline via pipelineContext.args"          optional:"" passthrough:""`
	ServerURL  string        `env:"CI_SERVER_URL"                                 help:"URL of the CI server"                                               required:"" short:"s"`
	Timeout    time.Duration `env:"CI_TIMEOUT"                                    help:"Client-side timeout for the full execution (0 = no timeout)"`
	NoWorkdir  bool          `help:"Skip uploading the current working directory"`
	Ignore     []string      `default:".git/**/*"                                 help:"Glob patterns to exclude from the workdir upload (comma-separated)" sep:","`
	AuthToken  string        `env:"CI_AUTH_TOKEN"                                 help:"Bearer token for OAuth-authenticated servers"                       short:"t"`
	ConfigFile string        `env:"CI_AUTH_CONFIG"                                help:"Path to auth config file (default: ~/.pocketci/auth.config)"        short:"c"`
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

	serverURL := strings.TrimSuffix(c.ServerURL, "/")
	endpoint := serverURL + "/api/pipelines/" + c.Name + "/run"

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

	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("could not rewind temp file: %w", err)
	}

	logger.Info("pipeline.run.trigger", "name", c.Name, "url", RedactURL(endpoint), "args", c.Args, "upload_bytes", bodySize)

	client, endpoint := c.configureClient(endpoint)

	resp, err := c.sendRequest(client, endpoint, tmpFile, contentType, bodySize)
	if err != nil {
		return err
	}
	defer func() { _ = resp.RawBody().Close() }()

	if err := checkResponseStatus(resp, serverURL); err != nil {
		return err
	}

	logger.Info("pipeline.run.streaming")

	return streamSSEEvents(resp.RawBody(), logger)
}

func (c *Run) buildMultipartBody(logger *slog.Logger) (*os.File, string, error) {
	tmpFile, err := os.CreateTemp("", "ci-upload-*.bin")
	if err != nil {
		return nil, "", fmt.Errorf("could not create temp file: %w", err)
	}

	mw := multipart.NewWriter(tmpFile)

	if err := c.writeArgsField(mw); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())

		return nil, "", err
	}

	if !c.NoWorkdir {
		if err := c.writeWorkdirField(mw, logger); err != nil {
			_ = tmpFile.Close()
			_ = os.Remove(tmpFile.Name())

			return nil, "", err
		}
	}

	if err := mw.Close(); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())

		return nil, "", fmt.Errorf("could not close multipart writer: %w", err)
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

	if _, err = fw.Write(argsData); err != nil {
		return fmt.Errorf("could not write args: %w", err)
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

	if err := tarDirectory(cwd, zw, c.Ignore); err != nil {
		return fmt.Errorf("could not tar working directory: %w", err)
	}

	if err := zw.Close(); err != nil {
		return fmt.Errorf("could not flush zstd stream: %w", err)
	}

	return nil
}

func (c *Run) configureClient(endpoint string) (*resty.Client, string) {
	client := resty.New()

	if parsed, err := url.Parse(endpoint); err == nil && parsed.User != nil {
		password, _ := parsed.User.Password()
		client.SetBasicAuth(parsed.User.Username(), password)
		parsed.User = nil
		endpoint = parsed.String()
	}

	token := ResolveAuthToken(c.AuthToken, c.ConfigFile, c.ServerURL)
	if token != "" {
		client.SetAuthToken(token)
	}

	if c.Timeout > 0 {
		client.SetTimeout(c.Timeout)
	}

	return client, endpoint
}

func (c *Run) sendRequest(client *resty.Client, endpoint string, body *os.File, contentType string, bodySize int64) (*resty.Response, error) {
	bar := progressbar.NewOptions64(bodySize,
		progressbar.OptionSetDescription("uploading"),
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionShowBytes(true),
		progressbar.OptionSetVisibility(!c.NoWorkdir),
		progressbar.OptionOnCompletion(func() { fmt.Fprintln(os.Stderr) }),
	)

	resp, err := client.R().
		SetHeader("Content-Type", contentType).
		SetHeader("Content-Length", strconv.FormatInt(bodySize, 10)).
		SetHeader("Accept", "text/event-stream").
		SetBody(io.TeeReader(body, bar)).
		SetDoNotParseResponse(true).
		Post(endpoint)
	if err != nil {
		return nil, fmt.Errorf("could not connect to server: %w", err)
	}

	_ = bar.Finish()

	return resp, nil
}

func checkResponseStatus(resp *resty.Response, serverURL string) error {
	if resp.StatusCode() == 401 {
		return authRequiredError(serverURL)
	}

	if resp.StatusCode() == 403 {
		return accessDeniedError(serverURL)
	}

	if resp.StatusCode() != 200 {
		body, _ := io.ReadAll(resp.RawBody())
		return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(body))
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
		if err := json.Unmarshal([]byte(payload), &evt); err != nil {
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

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading stream: %w", err)
	}

	return nil
}

// tarDirectory writes a tar archive of dir to w, compressing with zstd.
// ignorePatterns is a list of doublestar glob patterns (relative to dir) to skip.
func tarDirectory(dir string, w io.Writer, ignorePatterns []string) error {
	tw := tar.NewWriter(w)
	defer func() { _ = tw.Close() }()

	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		// Skip the root "." directory entry — extractors handle it implicitly.
		if relPath == "." {
			return nil
		}

		if len(ignorePatterns) > 0 {
			if ignorePath(relPath, info.IsDir(), ignorePatterns) {
				if info.IsDir() {
					return filepath.SkipDir
				}

				return nil
			}
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
		hdr.Xattrs = nil     //nolint:staticcheck // clear macOS xattrs
		hdr.PAXRecords = nil // avoid PAX extensions
		hdr.Format = tar.FormatGNU

		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("could not write tar header for %q: %w", relPath, err)
		}

		if !info.IsDir() {
			f, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("could not open %q: %w", path, err)
			}
			defer func() { _ = f.Close() }()

			if _, err = io.Copy(tw, f); err != nil {
				return fmt.Errorf("could not write %q to tar: %w", relPath, err)
			}
		}

		return nil
	})
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
