package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/dop251/goja"
)

// asyncTask runs work in a goroutine and resolves a JS promise with the
// transformed result on the main thread. It handles promise lifecycle
// (Add/Done), panic recovery, and resolve/reject dispatch uniformly.
//
// work executes in a separate goroutine.
// transform runs on the main JS thread (via the tasks channel) and converts
// the work result into the value passed to JS resolve.
func asyncTask[T any](
	r *Runtime,
	label string,
	work func(context.Context) (T, error),
	transform func(T) (any, error),
) *goja.Promise {
	promise, resolve, reject := r.jsVM.NewPromise()

	r.promises.Add(1)

	ctx := r.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	go func() {
		defer func() {
			if p := recover(); p != nil {
				slog.Error(label+".panic", "panic", p, "stack", string(debug.Stack()))

				r.tasks <- func() error {
					defer r.promises.Done()

					return reject(r.jsVM.NewGoError(fmt.Errorf("panic in %s: %v", label, p)))
				}
			}
		}()

		result, err := work(ctx)

		r.tasks <- func() error {
			defer r.promises.Done()

			if err != nil {
				if rErr := reject(r.jsVM.NewGoError(err)); rErr != nil {
					return fmt.Errorf("could not reject %s: %w", label, rErr)
				}

				return nil
			}

			jsVal, tErr := transform(result)
			if tErr != nil {
				if rErr := reject(r.jsVM.NewGoError(tErr)); rErr != nil {
					return fmt.Errorf("could not reject %s: %w", label, rErr)
				}

				return nil
			}

			if rErr := resolve(jsVal); rErr != nil {
				return fmt.Errorf("could not resolve %s: %w", label, rErr)
			}

			return nil
		}
	}()

	return promise
}

// identity returns a transform function that passes the value through unchanged.
func identity[T any](v T) (any, error) { return v, nil }

// rejectImmediate creates a promise that is immediately rejected with the given
// error. Useful for synchronous validation failures before async work begins.
func (r *Runtime) rejectImmediate(err error) goja.Value {
	promise, _, reject := r.jsVM.NewPromise()
	_ = reject(r.jsVM.NewGoError(err))

	return r.jsVM.ToValue(promise)
}

// extractJSCallback reads a named property from a goja object and returns the
// callable if it exists and is a function; otherwise returns nil.
func extractJSCallback(vm *goja.Runtime, obj *goja.Object, name string) goja.Callable {
	val := obj.Get(name)
	if val == nil || goja.IsUndefined(val) || goja.IsNull(val) {
		return nil
	}

	fn, ok := goja.AssertFunction(val)
	if !ok {
		return nil
	}

	return fn
}

// exportFilePathsArray converts a JS array argument to a Go string slice.
func exportFilePathsArray(arg goja.Value) ([]string, bool) {
	arr, ok := arg.Export().([]interface{})
	if !ok {
		return nil, false
	}

	filePaths := make([]string, 0, len(arr))

	for _, val := range arr {
		filePaths = append(filePaths, fmt.Sprintf("%v", val))
	}

	return filePaths, true
}
