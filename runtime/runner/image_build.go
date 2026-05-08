package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// BuildKitDefaultImage is the container image used to perform builds.
// v1 uses the privileged variant — rootless mode requires Task.SecurityOpts
// which is not yet plumbed through the orchestra layer.
const BuildKitDefaultImage = "moby/buildkit:latest"

// BuildImageInput is the input to BuildImage.
type BuildImageInput struct {
	// Name is the build step's name (used for storage key + container ID).
	Name string `json:"name"`

	// Context is the path (relative to the build container's WorkDir) that
	// serves as the Docker build context. Typically the name of an input
	// volume mounted via Inputs.
	Context string `json:"context"`

	// Dockerfile is the path to the Dockerfile, relative to Context.
	// Defaults to "Dockerfile".
	Dockerfile string `json:"dockerfile"`

	// Tag is the image reference (registry/name:tag) to produce.
	Tag string `json:"tag"`

	// Push, when true, pushes the built image to its registry.
	Push bool `json:"push"`

	// BuildArgs are passed as --opt build-arg:KEY=VALUE.
	BuildArgs map[string]string `json:"buildArgs"`

	// Target is the optional Dockerfile build stage.
	Target string `json:"target"`

	// Platforms is a list of target platforms (e.g. linux/amd64).
	// If empty, host platform is used.
	Platforms []string `json:"platforms"`

	// Image is the buildkit image to use. Defaults to BuildKitDefaultImage.
	Image string `json:"image"`

	// Inputs maps input volume names to VolumeResults. Each entry is mounted
	// at <WorkDir>/<name> inside the build container.
	Inputs map[string]VolumeResult `json:"inputs"`

	// Caches maps cache mount paths (e.g. "/var/lib/buildkit") to VolumeResults.
	Caches map[string]VolumeResult `json:"caches"`

	// Env is additional environment for the build container. Values may
	// contain "secret:KEY" references that the runner resolves automatically.
	Env map[string]string `json:"env"`

	// RegistryAuth, when set, configures registry credentials. Username and
	// password are placed into env vars (secret-resolvable) and written into
	// /root/.docker/config.json by the build container.
	RegistryAuth *BuildImageRegistryAuth `json:"registryAuth"`

	// Timeout is an optional duration string (e.g. "10m") for the build.
	Timeout string `json:"timeout"`

	// Limits applies CPU/memory/CPU-kind to the build container. Required on
	// drivers like Fly.io where the default machine is too small to run
	// buildkit comfortably (256MB shared CPU).
	Limits BuildImageLimits `json:"limits"`

	// OnOutput streams build output to the caller in real time.
	OnOutput OutputCallback `json:"-"`

	// StorageKey overrides the auto-generated tasks/<callIndex>-<name> key.
	StorageKey string `json:"storageKey"`
}

// BuildImageLimits sets CPU/memory/CPU-kind on the build container. Driver
// support varies — Fly.io honors all three; Docker honors CPU shares and
// memory but ignores CPU-kind; the native driver ignores all of them.
type BuildImageLimits struct {
	CPU     int64  `json:"cpu"`
	CPUKind string `json:"cpuKind"`
	Memory  int64  `json:"memory"`
}

// BuildImageRegistryAuth holds credentials for a single registry. Username
// and password may be "secret:KEY" references; they are passed through the
// runner's standard env-secret resolution before reaching the build container.
type BuildImageRegistryAuth struct {
	// Registry is the hostname (e.g. "registry.example.com"). If empty, the
	// registry is inferred from Tag.
	Registry string `json:"registry"`
	Username string `json:"username"`
	Password string `json:"password"`
	// Insecure permits plain-HTTP and self-signed certificate connections to
	// the registry when pushing. Default false. Used for local test registries.
	Insecure bool `json:"insecure"`
}

// BuildImageResult is returned on a successful build.
type BuildImageResult struct {
	// Ref is the image reference produced by the build (= input.Tag).
	Ref string `json:"ref"`
	// Digest is the OCI image digest (sha256:...) extracted from buildkit's
	// metadata file. Empty if extraction fails.
	Digest string `json:"digest"`
	// RunResult is the underlying container run result (logs, exit code).
	RunResult *RunResult `json:"runResult"`
}

// BuildImage compiles a build_image step into a RunInput and dispatches it
// through the given runner. Both PipelineRunner and ResumableRunner are
// supported because BuildImage uses only the public Runner.Run surface.
func BuildImage(runner Runner, input BuildImageInput) (*BuildImageResult, error) {
	err := validateBuildImageInput(input)
	if err != nil {
		return nil, err
	}

	runInput := buildImageRunInput(input)

	result, err := runner.Run(runInput)
	if err != nil {
		return nil, fmt.Errorf("build image %q: %w", input.Tag, err)
	}

	if result.Status == RunAbort {
		return &BuildImageResult{Ref: input.Tag, RunResult: result}, nil
	}

	if result.Code != 0 {
		return &BuildImageResult{Ref: input.Tag, RunResult: result},
			fmt.Errorf("build image %q failed with exit code %d", input.Tag, result.Code)
	}

	return &BuildImageResult{
		Ref:       input.Tag,
		Digest:    extractBuildImageDigest(result.Stdout),
		RunResult: result,
	}, nil
}

// buildImageRunInput translates a BuildImageInput into the RunInput that the
// underlying runner expects: image, env (with registry creds), mounts,
// privilege flag, and the shell command that drives buildctl-daemonless.sh.
func buildImageRunInput(input BuildImageInput) RunInput {
	if input.Image == "" {
		input.Image = BuildKitDefaultImage
	}

	dockerfile := input.Dockerfile
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}

	contextPath := input.Context
	if !strings.HasPrefix(contextPath, "/") && !strings.HasPrefix(contextPath, "./") {
		contextPath = "./" + contextPath
	}

	dfDir := path.Join(contextPath, path.Dir(dockerfile))
	dfName := path.Base(dockerfile)

	registry := ""
	if input.RegistryAuth != nil {
		registry = input.RegistryAuth.Registry
	}

	if registry == "" {
		registry = registryFromTag(input.Tag)
	}

	script := buildImageScript(input, contextPath, dfDir, dfName, registry)

	runInput := RunInput{
		Name:       input.Name,
		Image:      input.Image,
		Privileged: true,
		Env:        buildImageEnv(input),
		Mounts:     buildImageMounts(input),
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

func buildImageEnv(input BuildImageInput) map[string]string {
	env := make(map[string]string, len(input.Env)+2)
	for k, v := range input.Env {
		env[k] = v
	}

	if input.RegistryAuth != nil {
		if input.RegistryAuth.Username != "" {
			env["POCKETCI_BUILDKIT_USERNAME"] = input.RegistryAuth.Username
		}

		if input.RegistryAuth.Password != "" {
			env["POCKETCI_BUILDKIT_PASSWORD"] = input.RegistryAuth.Password
		}
	}

	return env
}

func buildImageMounts(input BuildImageInput) map[string]VolumeResult {
	mounts := make(map[string]VolumeResult, len(input.Inputs)+len(input.Caches))
	for name, vol := range input.Inputs {
		mounts[name] = vol
	}

	for cachePath, vol := range input.Caches {
		mounts[cachePath] = vol
	}

	return mounts
}

func validateBuildImageInput(input BuildImageInput) error {
	if input.Tag == "" {
		return errors.New("build image: tag is required")
	}

	if input.Context == "" {
		return errors.New("build image: context is required")
	}

	if strings.Contains(input.Tag, " ") || strings.ContainsAny(input.Tag, "\n\r\t") {
		return fmt.Errorf("build image: tag %q contains whitespace", input.Tag)
	}

	return nil
}

// buildImageScript returns the shell program that runs inside moby/buildkit.
// All non-secret parameters are interpolated into the script directly; only
// username/password are passed via env so they go through secret resolution.
func buildImageScript(input BuildImageInput, contextPath, dfDir, dfName, registry string) string {
	push := "false"
	if input.Push {
		push = "true"
	}

	output := fmt.Sprintf("type=image,name=%s,push=%s", input.Tag, push)
	if input.RegistryAuth != nil && input.RegistryAuth.Insecure {
		output += ",registry.insecure=true"
	}

	parts := []string{
		"buildctl-daemonless.sh", "build",
		"--frontend", "dockerfile.v0",
		"--local", "context=" + shellQuote(contextPath),
		"--local", "dockerfile=" + shellQuote(dfDir),
		"--opt", "filename=" + shellQuote(dfName),
		"--output", shellQuote(output),
	}

	if input.Target != "" {
		parts = append(parts, "--opt", "target="+shellQuote(input.Target))
	}

	for _, plat := range input.Platforms {
		parts = append(parts, "--opt", "platform="+shellQuote(plat))
	}

	keys := make([]string, 0, len(input.BuildArgs))
	for k := range input.BuildArgs {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	for _, k := range keys {
		parts = append(parts, "--opt", shellQuote(fmt.Sprintf("build-arg:%s=%s", k, input.BuildArgs[k])))
	}

	parts = append(parts, "--metadata-file", "/tmp/pocketci-buildkit-meta.json")

	var b strings.Builder

	b.WriteString("set -eu\n")
	// Debug: report whether /var/lib/buildkit was restored from cache.
	b.WriteString(`echo "[debug] /var/lib/buildkit contents:"; ls -la /var/lib/buildkit/ 2>/dev/null | head -20 || echo "[debug] dir missing"` + "\n")
	b.WriteString(`echo "[debug] /var/lib/buildkit total size: $(du -sh /var/lib/buildkit 2>/dev/null || echo unknown)"` + "\n")

	if registry != "" && input.RegistryAuth != nil &&
		(input.RegistryAuth.Username != "" || input.RegistryAuth.Password != "") {
		b.WriteString("mkdir -p /root/.docker\n")
		b.WriteString(`auth=$(printf '%s:%s' "${POCKETCI_BUILDKIT_USERNAME:-}" "${POCKETCI_BUILDKIT_PASSWORD:-}" | base64 | tr -d '\n')` + "\n")
		fmt.Fprintf(&b,
			`printf '{"auths":{%q:{"auth":"%%s"}}}\n' "$auth" > /root/.docker/config.json`+"\n",
			registry,
		)
	}

	b.WriteString(strings.Join(parts, " "))
	b.WriteString("\n")
	b.WriteString(`if [ -f /tmp/pocketci-buildkit-meta.json ]; then` + "\n")
	b.WriteString(`  echo POCKETCI_IMAGE_METADATA_BEGIN` + "\n")
	b.WriteString(`  cat /tmp/pocketci-buildkit-meta.json` + "\n")
	b.WriteString(`  echo` + "\n")
	b.WriteString(`  echo POCKETCI_IMAGE_METADATA_END` + "\n")
	b.WriteString(`fi` + "\n")

	return b.String()
}

// shellQuote single-quotes a string for safe inclusion in a /bin/sh command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// registryFromTag extracts the registry hostname from a full tag like
// "registry.example.com/repo:latest". Returns "docker.io" for unqualified
// references (e.g. "alpine:latest" or "library/alpine:latest").
func registryFromTag(tag string) string {
	slash := strings.Index(tag, "/")
	if slash == -1 {
		return "docker.io"
	}

	first := tag[:slash]
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		return first
	}

	return "docker.io"
}

var imageMetadataRegexp = regexp.MustCompile(`(?s)POCKETCI_IMAGE_METADATA_BEGIN\s*(\{.*?\})\s*POCKETCI_IMAGE_METADATA_END`)

// extractBuildImageDigest parses the metadata JSON emitted by the build
// script and returns the containerimage.digest value.
func extractBuildImageDigest(stdout string) string {
	match := imageMetadataRegexp.FindStringSubmatch(stdout)
	if len(match) < 2 {
		return ""
	}

	var meta map[string]any

	err := json.Unmarshal([]byte(match[1]), &meta)
	if err != nil {
		return ""
	}

	if digest, ok := meta["containerimage.digest"].(string); ok {
		return digest
	}

	return ""
}
