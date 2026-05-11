package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/dop251/goja"

	"github.com/jtarchie/pocketci/runtime/cacheconfig"
	"github.com/jtarchie/pocketci/runtime/runner"
)

// CacheNS is the runtime.cache namespace exposed to JS/TS pipelines. It
// runs cache restore/persist as ordinary container tasks (peakcom/s5cmd)
// so the /tasks UI shows them next to the build that produced them, and
// the bytes flow container → S3 without going through the pocketci
// server. The S3 backend is configured once at server/executor startup
// via CI_CACHE_S3_*; pipeline authors only supply the per-call key.
type CacheNS struct {
	rt  *Runtime
	cfg *cacheconfig.S3
}

// Restore pulls a cache archive from S3 and extracts it into the named
// volume. A cache miss surfaces as exit code 1 inside the task — the
// pipeline continues and the consuming task sees an empty cache.
func (c *CacheNS) Restore(call goja.FunctionCall) goja.Value {
	return c.invoke(call, runner.CacheRestoreDirection)
}

// Persist tars the volume contents, zstd-compresses, and uploads the
// archive to S3 under the configured key.
func (c *CacheNS) Persist(call goja.FunctionCall) goja.Value {
	return c.invoke(call, runner.CachePersistDirection)
}

func (c *CacheNS) invoke(call goja.FunctionCall, direction runner.CacheOpDirection) goja.Value {
	r := c.rt

	if c.cfg == nil {
		return r.rejectImmediate(fmt.Errorf("cache %s: no cache backend configured (set CI_CACHE_S3_BUCKET)", direction))
	}

	if len(call.Arguments) == 0 {
		return r.rejectImmediate(fmt.Errorf("cache %s requires an input object", direction))
	}

	inputObj := call.Arguments[0].ToObject(r.jsVM)

	volumeName, ok := cacheNSVolumeName(inputObj)
	if !ok {
		return r.rejectImmediate(fmt.Errorf("cache %s: volume is required", direction))
	}

	keyVal := inputObj.Get("key")
	if keyVal == nil || goja.IsUndefined(keyVal) {
		return r.rejectImmediate(fmt.Errorf("cache %s: key is required", direction))
	}

	key := keyVal.String()
	if key == "" {
		return r.rejectImmediate(fmt.Errorf("cache %s: key is required", direction))
	}

	input := runner.CacheOpInput{
		Name:            volumeName,
		Volume:          runner.VolumeResult{Name: volumeName},
		MountName:       volumeName,
		Direction:       direction,
		Endpoint:        c.cfg.Endpoint,
		Region:          c.cfg.Region,
		Bucket:          c.cfg.Bucket,
		Key:             cacheNSJoinKey(c.cfg.Prefix, key),
		AccessKeyID:     c.cfg.AccessKeyID,
		SecretAccessKey: c.cfg.SecretAccessKey,
	}

	if fn := extractJSCallback(r.jsVM, inputObj, "onOutput"); fn != nil {
		input.OnOutput = func(stream, data string) {
			r.tasks <- func() error {
				_, _ = fn(goja.Undefined(), r.jsVM.ToValue(stream), r.jsVM.ToValue(data))
				return nil
			}
		}
	}

	return r.jsVM.ToValue(asyncTask(r, "cache."+string(direction), func(_ context.Context) (*runner.RunResult, error) {
		if direction == runner.CacheRestoreDirection {
			return runner.CacheRestore(r.runner, input)
		}

		return runner.CachePersist(r.runner, input)
	}, identity))
}

// cacheNSVolumeName accepts either a JS volume object (with .name) or a
// bare string and returns the volume name.
func cacheNSVolumeName(obj *goja.Object) (string, bool) {
	v := obj.Get("volume")
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return "", false
	}

	if vol, ok := v.Export().(map[string]any); ok {
		if name, ok := vol["name"].(string); ok && name != "" {
			return name, true
		}
	}

	if name := v.String(); name != "" && name != "undefined" && name != "null" {
		return name, true
	}

	return "", false
}

// cacheNSJoinKey prepends the configured prefix to the user-supplied key
// and appends the .tar.zst suffix used by the YAML/backwards path. Both
// JS and YAML pipelines coexist under a single bucket layout so users can
// share cache keys across runtimes.
func cacheNSJoinKey(prefix, key string) string {
	if prefix == "" {
		return key + ".tar.zst"
	}

	return prefix + "/" + key + ".tar.zst"
}

// errCacheNSNotConfigured is the static error returned when callers try to
// use runtime.cache without setting CI_CACHE_S3_BUCKET. Reserved for tests.
var errCacheNSNotConfigured = errors.New("cache: no backend configured")
