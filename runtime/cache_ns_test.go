package runtime

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/dop251/goja"

	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/runtime/cacheconfig"
	"github.com/jtarchie/pocketci/runtime/runner"
	"github.com/jtarchie/pocketci/secrets"
)

// stubRunner captures the RunInput passed to Run so tests can assert what
// the cache namespace builds without launching a real container.
type stubRunner struct {
	mu       sync.Mutex
	inputs   []runner.RunInput
	exitCode int
}

func (s *stubRunner) Run(input runner.RunInput) (*runner.RunResult, error) {
	s.mu.Lock()
	s.inputs = append(s.inputs, input)
	s.mu.Unlock()

	return &runner.RunResult{Code: s.exitCode}, nil
}

func (s *stubRunner) CreateVolume(_ runner.VolumeInput) (*runner.VolumeResult, error) {
	return &runner.VolumeResult{}, nil
}

func (s *stubRunner) CleanupVolumes() error { return nil }

func (s *stubRunner) StartSandbox(_ runner.SandboxInput) (*runner.SandboxHandle, error) {
	return nil, errors.New("stub: sandbox not supported")
}

func (s *stubRunner) SetAgentFunc(_ runner.AgentFunc) {}

func (s *stubRunner) RunAgent(_ json.RawMessage) (json.RawMessage, error) {
	return nil, errors.New("stub: agent not supported")
}

func (s *stubRunner) ReadFilesFromVolume(_ string, _ ...string) (map[string]string, error) {
	return nil, errors.New("stub: readFiles not supported")
}

func (s *stubRunner) SetSecretsManager(_ secrets.Manager, _ string) {}

func (s *stubRunner) SetPreseededVolumes(_ map[string]orchestra.Volume) {}

func (s *stubRunner) SetOutputCallback(_ runner.OutputCallback) {}

func (s *stubRunner) capturedInputs() []runner.RunInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]runner.RunInput, len(s.inputs))
	copy(out, s.inputs)

	return out
}

// newTestRuntime builds a Runtime wired up to a stub runner so we can drive
// the JS cache namespace without a container backend.
func newTestRuntime(t *testing.T, cfg *cacheconfig.S3) (*Runtime, *stubRunner) {
	t.Helper()

	vm := goja.New()
	vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))

	stub := &stubRunner{}
	rt := NewRuntime(vm, stub, "test-ns", "test-run")
	rt.cacheS3 = cfg

	return rt, stub
}

// drainAsyncTasks pulls every pending callback off the runtime's tasks
// channel and runs it, mirroring what Runtime.Wait() would do at pipeline
// shutdown. Required because asyncTask spawns a goroutine that posts the
// promise resolution back through the channel.
func drainAsyncTasks(t *testing.T, rt *Runtime) {
	t.Helper()

	go func() {
		rt.promises.Wait()
		close(rt.tasks)
	}()

	for task := range rt.tasks {
		_ = task()
	}
}

// TestCacheNSRestoreBuildsCacheOpInput verifies the JS cache.restore() call
// produces a RunInput shaped like the cache_op restore task: the volume
// gets mounted, the image is peakcom/s5cmd, the env carries the S3 creds,
// and the command is a shell script doing tar+zstd+s5cmd.
func TestCacheNSRestoreBuildsCacheOpInput(t *testing.T) {
	cfg := &cacheconfig.S3{
		Endpoint:        "https://fly.storage.tigris.dev",
		Region:          "auto",
		Bucket:          "ci-cache",
		Prefix:          "team-a",
		AccessKeyID:     "AKIA",
		SecretAccessKey: "SECRET",
	}

	rt, stub := newTestRuntime(t, cfg)
	ns := rt.CacheNS()

	// Build a JS object { volume: { name: "node-modules" }, key: "deps" }
	// the same way a pipeline would construct it.
	obj := rt.jsVM.NewObject()
	volObj := rt.jsVM.NewObject()
	_ = volObj.Set("name", "node-modules")
	_ = obj.Set("volume", volObj)
	_ = obj.Set("key", "deps")

	_ = ns.Restore(goja.FunctionCall{Arguments: []goja.Value{obj}})
	drainAsyncTasks(t, rt)

	inputs := stub.capturedInputs()
	if len(inputs) != 1 {
		t.Fatalf("expected one cache_op call, got %d", len(inputs))
	}

	got := inputs[0]
	if got.Image != runner.CacheOpDefaultImage {
		t.Errorf("expected image %q, got %q", runner.CacheOpDefaultImage, got.Image)
	}

	if got.Env["AWS_ACCESS_KEY_ID"] != "AKIA" {
		t.Errorf("expected AKIA, got %q", got.Env["AWS_ACCESS_KEY_ID"])
	}

	if got.Env["AWS_SECRET_ACCESS_KEY"] != "SECRET" {
		t.Errorf("expected SECRET, got %q", got.Env["AWS_SECRET_ACCESS_KEY"])
	}

	mount, ok := got.Mounts["node-modules"]
	if !ok {
		t.Fatalf("expected node-modules mount, got %#v", got.Mounts)
	}

	if mount.Name != "node-modules" {
		t.Errorf("expected mount volume name node-modules, got %q", mount.Name)
	}

	if len(got.Command.Args) < 2 || got.Command.Path != "sh" {
		t.Fatalf("expected sh -c <script>, got %v %v", got.Command.Path, got.Command.Args)
	}

	script := got.Command.Args[1]
	// The cache_op restore script downloads then extracts; key collision
	// across pipelines is prevented by the prefix being part of the S3
	// URL, so we check both surfaces here.
	if !contains(script, "team-a/deps.tar.zst") {
		t.Errorf("expected script to reference team-a/deps.tar.zst, got: %s", script)
	}

	if !contains(script, "s5cmd") {
		t.Errorf("expected script to invoke s5cmd, got: %s", script)
	}
}

// TestCacheNSPersistDirection checks that Persist sets the persist
// direction in the generated script (tar | zstd | s5cmd pipe).
func TestCacheNSPersistDirection(t *testing.T) {
	cfg := &cacheconfig.S3{Bucket: "ci-cache"}
	rt, stub := newTestRuntime(t, cfg)
	ns := rt.CacheNS()

	obj := rt.jsVM.NewObject()
	_ = obj.Set("volume", "deps")
	_ = obj.Set("key", "deps")

	_ = ns.Persist(goja.FunctionCall{Arguments: []goja.Value{obj}})
	drainAsyncTasks(t, rt)

	inputs := stub.capturedInputs()
	if len(inputs) != 1 {
		t.Fatalf("expected one cache_op call, got %d", len(inputs))
	}

	script := inputs[0].Command.Args[1]
	if !contains(script, "tar cf -") || !contains(script, "s5cmd") {
		t.Errorf("expected persist script with tar+s5cmd, got: %s", script)
	}
}

// TestCacheNSNoBackendRejects verifies that calling cache.restore without a
// configured backend rejects rather than panicking.
func TestCacheNSNoBackendRejects(t *testing.T) {
	rt, stub := newTestRuntime(t, nil)
	ns := rt.CacheNS()

	obj := rt.jsVM.NewObject()
	_ = obj.Set("volume", "v")
	_ = obj.Set("key", "k")

	val := ns.Restore(goja.FunctionCall{Arguments: []goja.Value{obj}})

	promise, ok := val.Export().(*goja.Promise)
	if !ok {
		t.Fatalf("expected a Promise, got %T", val.Export())
	}

	if promise.State() != goja.PromiseStateRejected {
		t.Fatalf("expected rejected promise, got state %v", promise.State())
	}

	if len(stub.capturedInputs()) != 0 {
		t.Fatalf("expected no cache_op call when backend is unconfigured")
	}
}

// TestCacheNSMissingVolumeRejects guards the input validation: passing an
// object without a volume must reject, not panic.
func TestCacheNSMissingVolumeRejects(t *testing.T) {
	rt, _ := newTestRuntime(t, &cacheconfig.S3{Bucket: "b"})
	ns := rt.CacheNS()

	obj := rt.jsVM.NewObject()
	_ = obj.Set("key", "k")

	val := ns.Restore(goja.FunctionCall{Arguments: []goja.Value{obj}})
	promise := val.Export().(*goja.Promise)

	if promise.State() != goja.PromiseStateRejected {
		t.Fatalf("expected rejected promise when volume is missing")
	}
}

// contains is a tiny strings.Contains alias so the test file doesn't need
// to grow a strings import for one use.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}

	return false
}
