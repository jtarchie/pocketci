package runner

import (
	"errors"
	"fmt"
	"strings"
)

// CacheOpDefaultImage is the container image used to push/pull cache
// archives to/from S3-compatible storage. The official AWS CLI image
// already bundles `aws`, `tar`, `gzip`, and `sh`; every orchestra driver
// overrides the image entrypoint so we can run an arbitrary shell
// command in it.
const CacheOpDefaultImage = "amazon/aws-cli:latest"

// CacheOpDirection identifies whether a cache op pushes (persist) or
// pulls (restore) data.
type CacheOpDirection string

const (
	CacheRestoreDirection CacheOpDirection = "restore"
	CachePersistDirection CacheOpDirection = "persist"
)

// CacheOpInput configures a single cache restore or persist task. The
// task runs in CacheOpDefaultImage with the named volume mounted under
// /workspace/<MountName>; tar+gzip+aws-s3-cp do the streaming.
type CacheOpInput struct {
	// Name is the display name (used in logs + container ID).
	Name string `json:"name"`

	// Volume is the cache volume to read from (persist) or write into
	// (restore). It is mounted under /workspace/<MountName>.
	Volume VolumeResult `json:"volume"`

	// MountName is the directory name of the volume inside /workspace.
	// Defaults to Volume.Name when empty.
	MountName string `json:"mountName"`

	// Direction is "restore" or "persist".
	Direction CacheOpDirection `json:"direction"`

	// Endpoint is the S3-compatible endpoint URL (e.g. https://fly.storage.tigris.dev).
	// When empty, AWS S3 is targeted with no --endpoint-url flag.
	Endpoint string `json:"endpoint"`

	// Bucket is the S3 bucket name.
	Bucket string `json:"bucket"`

	// Key is the full object key (no leading slash) within the bucket.
	Key string `json:"key"`

	// AccessKeyID and SecretAccessKey are the S3 credentials. Both may
	// use the "secret:KEY" prefix; the runner resolves them at task
	// launch the same way it does for any other env value.
	AccessKeyID     string `json:"accessKeyID"`
	SecretAccessKey string `json:"secretAccessKey"`

	// Region is the AWS region. Optional; aws-cli falls back to
	// "us-east-1" when unset, which Tigris and most S3-compat services
	// accept as a placeholder.
	Region string `json:"region"`

	// Image overrides the default CacheOpDefaultImage.
	Image string `json:"image"`

	// Env adds extra env vars (also subject to "secret:KEY" resolution).
	Env map[string]string `json:"env"`

	// Limits applies CPU/memory to the task container.
	Limits BuildImageLimits `json:"limits"`

	// Timeout is an optional duration string (e.g. "10m").
	Timeout string `json:"timeout"`

	// OnOutput streams stdout/stderr chunks to the caller in real time.
	OnOutput OutputCallback `json:"-"`

	// StorageKey overrides the auto-generated tasks/<callIndex>-<name>
	// storage path. Callers (e.g. the YAML JobRunner) supply the
	// /pipeline/<runID>/jobs/<jobName>/cache/<direction>/<volume> key.
	StorageKey string `json:"storageKey"`
}

// CacheRestore pulls a cache archive from S3 and extracts it into the
// volume. A cache miss exits zero — the caller's downstream task simply
// sees an empty cache directory.
func CacheRestore(runner Runner, input CacheOpInput) (*RunResult, error) {
	input.Direction = CacheRestoreDirection
	return runCacheOp(runner, input)
}

// CachePersist tars the volume contents, gzips, and uploads to S3 under
// the configured key.
func CachePersist(runner Runner, input CacheOpInput) (*RunResult, error) {
	input.Direction = CachePersistDirection
	return runCacheOp(runner, input)
}

func runCacheOp(runner Runner, input CacheOpInput) (*RunResult, error) {
	err := validateCacheOpInput(input)
	if err != nil {
		return nil, err
	}

	runInput := cacheOpRunInput(input)

	result, err := runner.Run(runInput)
	if err != nil {
		return nil, fmt.Errorf("cache %s %q: %w", input.Direction, input.Name, err)
	}

	if result.Status == RunAbort {
		return result, nil
	}

	if result.Code != 0 {
		return result, fmt.Errorf("cache %s %q failed with exit code %d", input.Direction, input.Name, result.Code)
	}

	return result, nil
}

func validateCacheOpInput(input CacheOpInput) error {
	if input.Name == "" {
		return errors.New("cache op: name is required")
	}

	if input.Volume.Name == "" {
		return errors.New("cache op: volume is required")
	}

	if input.Bucket == "" {
		return errors.New("cache op: bucket is required")
	}

	if input.Key == "" {
		return errors.New("cache op: key is required")
	}

	if input.Direction != CacheRestoreDirection && input.Direction != CachePersistDirection {
		return fmt.Errorf("cache op: direction must be %q or %q, got %q", CacheRestoreDirection, CachePersistDirection, input.Direction)
	}

	return nil
}

// cacheOpRunInput translates a CacheOpInput into a RunInput suitable for
// PipelineRunner.Run.
func cacheOpRunInput(input CacheOpInput) RunInput {
	if input.Image == "" {
		input.Image = CacheOpDefaultImage
	}

	mountName := input.MountName
	if mountName == "" {
		mountName = input.Volume.Name
	}

	mounts := map[string]VolumeResult{mountName: input.Volume}
	env := cacheOpEnv(input)

	script := cacheOpScript(input, mountName)

	runInput := RunInput{
		Name:       input.Name,
		Image:      input.Image,
		Env:        env,
		Mounts:     mounts,
		OnOutput:   input.OnOutput,
		Timeout:    input.Timeout,
		StorageKey: input.StorageKey,
	}
	runInput.Command.Path = "sh"
	runInput.Command.Args = []string{"-c", script}
	runInput.ContainerLimits.CPU = input.Limits.CPU
	runInput.ContainerLimits.CPUKind = input.Limits.CPUKind
	runInput.ContainerLimits.Memory = input.Limits.Memory

	return runInput
}

func cacheOpEnv(input CacheOpInput) map[string]string {
	env := make(map[string]string, len(input.Env)+5)
	for k, v := range input.Env {
		env[k] = v
	}

	if input.AccessKeyID != "" {
		env["AWS_ACCESS_KEY_ID"] = input.AccessKeyID
	}

	if input.SecretAccessKey != "" {
		env["AWS_SECRET_ACCESS_KEY"] = input.SecretAccessKey
	}

	region := input.Region
	if region == "" {
		region = "us-east-1"
	}

	env["AWS_DEFAULT_REGION"] = region
	env["AWS_REGION"] = region

	return env
}

// cacheOpScript builds the shell command run inside the cache task
// container. It is split between persist and restore directions.
//
//   - persist:  tar cf - -C ./<vol> .  | gzip  | aws s3 cp -  s3://<bucket>/<key>  [--endpoint-url <ep>]
//   - restore:  aws s3 cp s3://<bucket>/<key> -  [--endpoint-url <ep>]  | gzip -d  | tar xf -  -C ./<vol>
//
// The whole pipeline runs on the cache task container, not on the
// pocketci server: the bytes flow container → S3 directly.
//
// The mount is referenced as a relative path from the working directory.
// Both Fly (workDir=/workspace, mount at /workspace/<vol>) and Docker
// (workDir=/tmp/<id>, mount at /tmp/<id>/<vol>) give us the volume at
// "./<vol>" relative to the default workDir, so the same script works
// across drivers.
func cacheOpScript(input CacheOpInput, mountName string) string {
	mountPath := "./" + mountName

	endpointFlag := ""
	if input.Endpoint != "" {
		endpointFlag = " --endpoint-url " + shellQuote(input.Endpoint)
	}

	s3Url := "s3://" + input.Bucket + "/" + input.Key

	var b strings.Builder

	b.WriteString("set -eu\n")
	// amazon/aws-cli ships with the AWS CLI, dnf, and a shell, but `tar`
	// and `gzip` are not always installed in the base image. Best-effort
	// install them on first run; a cached layer makes subsequent runs cheap.
	b.WriteString("if ! command -v tar >/dev/null 2>&1 || ! command -v gzip >/dev/null 2>&1; then\n")
	b.WriteString("  echo '[cache] installing tar/gzip via dnf'\n")
	b.WriteString("  dnf install -y tar gzip 1>/dev/null\n")
	b.WriteString("fi\n")

	switch input.Direction {
	case CachePersistDirection:
		b.WriteString("mkdir -p " + shellQuote(mountPath) + "\n")
		b.WriteString("echo '[cache] persisting " + mountName + " to " + s3Url + "'\n")
		b.WriteString("tar cf - -C " + shellQuote(mountPath) + " . | gzip | aws s3 cp - " + shellQuote(s3Url) + endpointFlag + "\n")
		b.WriteString("echo '[cache] persist complete'\n")

	case CacheRestoreDirection:
		b.WriteString("mkdir -p " + shellQuote(mountPath) + "\n")
		b.WriteString("echo '[cache] restoring " + mountName + " from " + s3Url + "'\n")
		// A cache miss returns a non-zero exit from `aws s3 cp`; treat it as
		// a no-op, not a task failure. Other errors still propagate via
		// `set -eu` once we see the cp command actually started downloading.
		b.WriteString("if aws s3 cp " + shellQuote(s3Url) + " - " + endpointFlag + " 2>/tmp/cache-stderr | gzip -d | tar xf - -C " + shellQuote(mountPath) + "; then\n")
		b.WriteString("  echo '[cache] restore complete'\n")
		b.WriteString("else\n")
		b.WriteString("  if grep -q -E 'Not Found|NoSuchKey|404' /tmp/cache-stderr 2>/dev/null; then\n")
		b.WriteString("    echo '[cache] miss (no prior data)'\n")
		b.WriteString("  else\n")
		b.WriteString("    echo '[cache] restore failed:'\n")
		b.WriteString("    cat /tmp/cache-stderr 1>&2\n")
		b.WriteString("    exit 1\n")
		b.WriteString("  fi\n")
		b.WriteString("fi\n")
	}

	return b.String()
}
