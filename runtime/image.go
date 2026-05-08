package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/dop251/goja"

	"github.com/jtarchie/pocketci/runtime/runner"
)

// Image exposes container image building to JavaScript as the `image` namespace.
// It holds a reference to the parent Runtime to share the underlying runner.
type Image struct {
	rt *Runtime
}

// ImageNS returns an Image namespace instance sharing this runtime's state.
func (r *Runtime) ImageNS() *Image {
	return &Image{rt: r}
}

// Build runs a container image build using moby/buildkit. It accepts an
// object with the same shape as runner.BuildImageInput and returns a Promise
// resolving to { ref, digest } on success.
func (i *Image) Build(call goja.FunctionCall) goja.Value {
	r := i.rt

	if len(call.Arguments) == 0 {
		return r.rejectImmediate(errors.New("image.build requires an input object"))
	}

	inputObj := call.Arguments[0].ToObject(r.jsVM)

	var input runner.BuildImageInput

	err := r.jsVM.ExportTo(inputObj, &input)
	if err != nil {
		return r.rejectImmediate(fmt.Errorf("invalid image.build input: %w", err))
	}

	if fn := extractJSCallback(r.jsVM, inputObj, "onOutput"); fn != nil {
		input.OnOutput = func(stream string, data string) {
			r.tasks <- func() error {
				_, _ = fn(goja.Undefined(), r.jsVM.ToValue(stream), r.jsVM.ToValue(data))
				return nil
			}
		}
	}

	return r.jsVM.ToValue(asyncTask(r, "image.build",
		func(_ context.Context) (*runner.BuildImageResult, error) {
			return runner.BuildImage(r.runner, input)
		},
		func(result *runner.BuildImageResult) (any, error) {
			obj := r.jsVM.NewObject()
			_ = obj.Set("ref", result.Ref)
			_ = obj.Set("digest", result.Digest)

			if result.RunResult != nil {
				_ = obj.Set("code", result.RunResult.Code)
				_ = obj.Set("stdout", result.RunResult.Stdout)
				_ = obj.Set("stderr", result.RunResult.Stderr)
			}

			return obj, nil
		}))
}
