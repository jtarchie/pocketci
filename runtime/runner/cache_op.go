package runner

import (
	"errors"
	"fmt"
	"strings"
)

// CacheOpDefaultImage is the container image used to push/pull cache
// archives to/from S3-compatible storage. peakcom/s5cmd is alpine-based
// (~12.5 MB) and ships s5cmd preinstalled. Busybox provides tar/gzip/sh;
// zstd is the only runtime apk install. Compare amazon/aws-cli at ~400 MB
// with dnf + curl-install of s5cmd that the previous image required.
// Every orchestra driver overrides the image entrypoint so we can run an
// arbitrary shell command in it.
const CacheOpDefaultImage = "peakcom/s5cmd:v2.3.0"

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
//   - persist:  tar cf - -C ./<vol> .       | zstd -T0   | s5cmd [--endpoint-url <ep>] pipe --concurrency 10 s3://<bucket>/<key>
//   - restore:  s5cmd [--endpoint-url <ep>] cp s3://<bucket>/<key> ./cache.tar.zst  →  zstd -d ./cache.tar.zst | tar xf - -C ./<vol>
//
// Both directions use s5cmd. Persist streams stdin via `s5cmd pipe`
// (parallel multipart upload). Restore writes to a tmpfile on the
// workspace volume so `s5cmd cp` can parallel-multipart download via
// byte-range GETs — much faster than aws-cli's single-stream stdin
// stdout. The intermediate file is removed immediately after extract.
//
// Compression uses zstd with -T0 so it parallelises across every CPU
// core allocated to the task — multi-GB caches are compress-bound,
// not S3-bound. zstd also decompresses several times faster than gzip,
// which matters on the restore-then-build hot path.
//
// Image: peakcom/s5cmd is alpine-based with s5cmd preinstalled and
// busybox tar/gzip already on PATH. zstd is the only runtime install
// (apk add, ~1–2 s).
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

	// s5cmd takes --endpoint-url as a global flag, before the subcommand.
	endpointFlag := ""
	if input.Endpoint != "" {
		endpointFlag = "--endpoint-url " + shellQuote(input.Endpoint) + " "
	}

	s3Url := "s3://" + input.Bucket + "/" + input.Key

	var b strings.Builder

	b.WriteString("set -eu\n")
	// pipefail so a failing zstd in `zstd -dc | tar xf -` propagates;
	// without it, tar's exit code masks earlier decoder failures.
	b.WriteString("set -o pipefail\n")
	// peakcom/s5cmd ships the binary at /s5cmd, not on PATH. Symlink
	// it into /usr/local/bin so `s5cmd` resolves below. Stays a no-op
	// for images where s5cmd is already on PATH.
	b.WriteString("if ! command -v s5cmd >/dev/null 2>&1 && [ -x /s5cmd ]; then\n")
	b.WriteString("  ln -sf /s5cmd /usr/local/bin/s5cmd\n")
	b.WriteString("fi\n")
	// Alpine busybox already provides tar+gzip+sh; only zstd needs
	// installing. apk add is ~1–2 s versus dnf install at 5–15 s.
	b.WriteString("if ! command -v zstd >/dev/null 2>&1; then\n")
	b.WriteString("  echo '[cache] installing zstd via apk'\n")
	b.WriteString("  apk add --no-cache zstd 1>/dev/null\n")
	b.WriteString("fi\n")

	switch input.Direction {
	case CachePersistDirection:
		b.WriteString("mkdir -p " + shellQuote(mountPath) + "\n")
		b.WriteString("echo '[cache] persisting " + mountName + " to " + s3Url + "'\n")
		b.WriteString("tar cf - -C " + shellQuote(mountPath) + " . | zstd -T0 | s5cmd " + endpointFlag + "pipe --concurrency 10 " + shellQuote(s3Url) + "\n")
		b.WriteString("echo '[cache] persist complete'\n")

	case CacheRestoreDirection:
		b.WriteString("mkdir -p " + shellQuote(mountPath) + "\n")
		b.WriteString("echo '[cache] restoring " + mountName + " from " + s3Url + "'\n")
		// Distinguish three outcomes via exit code so the /tasks UI can
		// show whether the cache hit, missed, or hit a transport error:
		//   exit 0 → restore complete (cache hit)
		//   exit 1 → cache miss (no prior data) — visible as a "failure"
		//             status, but the job continues; the consuming task
		//             starts with an empty cache and repopulates it.
		//   exit 2 → transport error (auth, network, corrupt archive) —
		//             also "failure", but distinct so operators can spot
		//             a real problem versus a cold cache.
		//
		// The download lands on the workspace volume (./cache.tar.zst)
		// rather than /tmp because the Fly rootfs is small (~1 GB) and
		// a multi-GB cache would overflow it. The tmpfile is removed
		// after extract.
		//
		// Download and extract are separate stages. Splitting them lets
		// us tell a transport error (download failed) apart from
		// archive corruption (download fine, decode/untar failed). On
		// extract failure we also wipe the volume contents so the
		// downstream consumer starts from an empty cache instead of
		// seeing a half-applied extract mixed with stale state from a
		// prior good run.
		b.WriteString("if ! s5cmd " + endpointFlag + "cp " + shellQuote(s3Url) + " ./cache.tar.zst 2>/tmp/cache-stderr; then\n")
		b.WriteString("  rm -f ./cache.tar.zst\n")
		// s5cmd surfaces the underlying S3 error code in its stderr, so
		// the same NoSuchKey/404 patterns the aws-cli path used still
		// match a missing key today. "object not found" is added because
		// s5cmd v2.x sometimes phrases misses that way for list/stat-
		// style lookups before the cp.
		b.WriteString("  if grep -q -E 'Not Found|NoSuchKey|404|object not found' /tmp/cache-stderr 2>/dev/null; then\n")
		b.WriteString("    echo '[cache] miss (no prior data)'\n")
		b.WriteString("    exit 1\n")
		b.WriteString("  fi\n")
		b.WriteString("  echo '[cache] restore download failed:' 1>&2\n")
		b.WriteString("  cat /tmp/cache-stderr 1>&2\n")
		b.WriteString("  exit 2\n")
		b.WriteString("fi\n")
		// Stage 2: decode + extract. On failure, wipe the volume so the
		// downstream consumer sees a clean miss rather than partial
		// state.
		b.WriteString("if ! zstd -dc ./cache.tar.zst | tar xf - -C " + shellQuote(mountPath) + " 2>/tmp/cache-extract-err; then\n")
		b.WriteString("  rm -rf " + shellQuote(mountPath) + "/* " + shellQuote(mountPath) + "/.[!.]* 2>/dev/null || true\n")
		b.WriteString("  rm -f ./cache.tar.zst\n")
		b.WriteString("  echo '[cache] restore extract failed (likely corrupted archive); volume cleared' 1>&2\n")
		b.WriteString("  cat /tmp/cache-extract-err 1>&2 || true\n")
		b.WriteString("  exit 2\n")
		b.WriteString("fi\n")
		b.WriteString("rm -f ./cache.tar.zst\n")
		b.WriteString("echo '[cache] restore complete'\n")
	}

	return b.String()
}
