package runtime

import (
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"

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

	promise, resolve, reject := r.jsVM.NewPromise()

	r.promises.Add(1)

	go func() {
		defer func() {
			if p := recover(); p != nil {
				slog.Error("volumes.create.panic", "panic", p, "stack", string(debug.Stack()))
				r.tasks <- func() error {
					defer r.promises.Done()
					return reject(r.jsVM.NewGoError(fmt.Errorf("panic in createVolume: %v", p)))
				}
			}
		}()

		result, err := r.runner.CreateVolume(input)

		r.tasks <- func() error {
			defer r.promises.Done()

			if err != nil {
				err = reject(err)
				if err != nil {
					return fmt.Errorf("could not reject run: %w", err)
				}

				return nil
			}

			volObj := v.buildVolumeObject(result)

			err := resolve(volObj)
			if err != nil {
				return fmt.Errorf("could not resolve create volume: %w", err)
			}

			return nil
		}
	}()

	return promise
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
		promise, resolve, reject := r.jsVM.NewPromise()

		if len(call.Arguments) < 1 {
			_ = reject(r.jsVM.NewGoError(errors.New("readFiles requires a filePaths array")))
			return r.jsVM.ToValue(promise)
		}

		var filePaths []string
		if arr, ok := call.Arguments[0].Export().([]interface{}); ok {
			filePaths = make([]string, 0, len(arr))
			for _, val := range arr {
				filePaths = append(filePaths, fmt.Sprintf("%v", val))
			}
		} else {
			_ = reject(r.jsVM.NewGoError(errors.New("readFiles expects an array of file paths")))
			return r.jsVM.ToValue(promise)
		}

		r.promises.Add(1)

		go func() {
			defer func() {
				if p := recover(); p != nil {
					slog.Error("volume.readFiles.panic", "panic", p, "stack", string(debug.Stack()))
					r.tasks <- func() error {
						defer r.promises.Done()
						return reject(r.jsVM.NewGoError(fmt.Errorf("panic in readFiles: %v", p)))
					}
				}
			}()

			result, err := r.runner.ReadFilesFromVolume(volumeName, filePaths...)

			r.tasks <- func() error {
				defer r.promises.Done()

				if err != nil {
					err = reject(r.jsVM.NewGoError(err))
					if err != nil {
						return fmt.Errorf("could not reject readFiles: %w", err)
					}

					return nil
				}

				err = resolve(result)
				if err != nil {
					return fmt.Errorf("could not resolve readFiles: %w", err)
				}

				return nil
			}
		}()

		return r.jsVM.ToValue(promise)
	}
}

// ReadFiles reads specific files from a named volume.
// Returns a Promise that resolves to a map of path → content strings.
func (v *Volumes) ReadFiles(call goja.FunctionCall) goja.Value {
	r := v.rt
	promise, resolve, reject := r.jsVM.NewPromise()

	if len(call.Arguments) < 2 {
		_ = reject(r.jsVM.NewGoError(errors.New("readFiles requires volumeName and filePaths")))
		return r.jsVM.ToValue(promise)
	}

	volumeName := call.Arguments[0].String()

	// Support both array and variadic string arguments
	var filePaths []string
	if arr, ok := call.Arguments[1].Export().([]interface{}); ok {
		filePaths = make([]string, 0, len(arr))
		for _, val := range arr {
			filePaths = append(filePaths, fmt.Sprintf("%v", val))
		}
	} else {
		filePaths = make([]string, 0, len(call.Arguments)-1)
		for i := 1; i < len(call.Arguments); i++ {
			filePaths = append(filePaths, call.Arguments[i].String())
		}
	}

	r.promises.Add(1)

	go func() {
		defer func() {
			if p := recover(); p != nil {
				slog.Error("volumes.readFiles.panic", "panic", p, "stack", string(debug.Stack()))
				r.tasks <- func() error {
					defer r.promises.Done()
					return reject(r.jsVM.NewGoError(fmt.Errorf("panic in readFiles: %v", p)))
				}
			}
		}()

		result, err := r.runner.ReadFilesFromVolume(volumeName, filePaths...)

		r.tasks <- func() error {
			defer r.promises.Done()

			if err != nil {
				err = reject(r.jsVM.NewGoError(err))
				if err != nil {
					return fmt.Errorf("could not reject readFiles: %w", err)
				}

				return nil
			}

			err = resolve(result)
			if err != nil {
				return fmt.Errorf("could not resolve readFiles: %w", err)
			}

			return nil
		}
	}()

	return r.jsVM.ToValue(promise)
}
