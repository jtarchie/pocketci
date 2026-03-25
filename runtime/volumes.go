package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/dop251/goja"

	"github.com/jtarchie/pocketci/runtime/runner"
	"github.com/jtarchie/pocketci/runtime/support"
)

// Volumes exposes volume management to JavaScript as the `volumes` namespace.
// It holds a reference to the parent Runtime to access shared state.
type Volumes struct {
	rt *Runtime
}

// Create creates a new volume.
func (v *Volumes) Create(input runner.VolumeInput) *goja.Promise {
	r := v.rt

	if input.Name == "" {
		r.mu.Lock()
		volumeID := fmt.Sprintf("vol-%d", r.volumeIndex)
		r.volumeIndex++
		r.mu.Unlock()
		input.Name = support.DeterministicVolumeID(r.namespace, fmt.Sprintf("%s-%s", r.runID, volumeID))
	}

	return asyncTask(r, "volumes.create", func(_ context.Context) (*runner.VolumeResult, error) {
		return r.runner.CreateVolume(input)
	}, func(result *runner.VolumeResult) (any, error) {
		return v.buildVolumeObject(result), nil
	})
}

// buildVolumeObject constructs a JS object with name, path, and a readFiles()
// method, following the same pattern as buildSandboxObject.
func (v *Volumes) buildVolumeObject(result *runner.VolumeResult) *goja.Object {
	r := v.rt
	obj := r.jsVM.NewObject()
	_ = obj.Set("name", result.Name)
	_ = obj.Set("path", result.Path)
	_ = obj.Set("readFiles", v.volumeReadFilesFunc(result.Name))

	return obj
}

// volumeReadFilesFunc returns a JS-callable function that reads files from the
// given volume name.
func (v *Volumes) volumeReadFilesFunc(volumeName string) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		r := v.rt

		if len(call.Arguments) < 1 {
			return r.rejectImmediate(errors.New("readFiles requires a filePaths array"))
		}

		filePaths, ok := exportFilePathsArray(call.Arguments[0])
		if !ok {
			return r.rejectImmediate(errors.New("readFiles expects an array of file paths"))
		}

		return r.jsVM.ToValue(asyncTask(r, "volume.readFiles", func(_ context.Context) (map[string]string, error) {
			return r.runner.ReadFilesFromVolume(volumeName, filePaths...)
		}, identity))
	}
}

// ReadFiles reads specific files from a named volume.
// Returns a Promise that resolves to a map of path → content strings.
func (v *Volumes) ReadFiles(call goja.FunctionCall) goja.Value {
	r := v.rt

	if len(call.Arguments) < 2 {
		return r.rejectImmediate(errors.New("readFiles requires volumeName and filePaths"))
	}

	volumeName := call.Arguments[0].String()

	// Support both array and variadic string arguments
	var filePaths []string
	if arr, ok := exportFilePathsArray(call.Arguments[1]); ok {
		filePaths = arr
	} else {
		filePaths = make([]string, 0, len(call.Arguments)-1)
		for i := 1; i < len(call.Arguments); i++ {
			filePaths = append(filePaths, call.Arguments[i].String())
		}
	}

	return r.jsVM.ToValue(asyncTask(r, "volumes.readFiles", func(_ context.Context) (map[string]string, error) {
		return r.runner.ReadFilesFromVolume(volumeName, filePaths...)
	}, identity))
}
