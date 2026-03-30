package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/google/uuid"
	"github.com/jtarchie/pocketci/storage"
)

// PipelineNamespace exposes pipeline.stage(), pipeline.job(), and pipeline.gate()
// to JavaScript. It holds a reference to the parent Runtime.
type PipelineNamespace struct {
	rt *Runtime
}

// stageScope tracks job registrations within a pipeline.stage() callback.
type stageScope struct {
	mu   sync.Mutex
	jobs []stageJob
}

type stageJob struct {
	name string
	fn   goja.Callable
}

func (s *stageScope) addJob(name string, fn goja.Callable) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.jobs = append(s.jobs, stageJob{name: name, fn: fn})
}

// Stage executes a stage: collects jobs from the callback, then runs them in parallel.
// Usage: await pipeline.stage("build", (stage) => { stage.job("compile", async () => {...}); })
func (p *PipelineNamespace) Stage(call goja.FunctionCall) goja.Value {
	r := p.rt

	if len(call.Arguments) < 2 {
		return r.rejectImmediate(errors.New("pipeline.stage requires (name, callback)"))
	}

	stageName := call.Arguments[0].String()

	callbackVal := call.Arguments[1]
	callback, ok := goja.AssertFunction(callbackVal)

	if !ok {
		return r.rejectImmediate(errors.New("pipeline.stage callback must be a function"))
	}

	scope := &stageScope{}

	// Build the stage object that gets passed to the callback
	stageObj := r.jsVM.NewObject()
	_ = stageObj.Set("job", func(jobCall goja.FunctionCall) goja.Value {
		if len(jobCall.Arguments) < 2 {
			panic(r.jsVM.NewGoError(errors.New("stage.job requires (name, callback)")))
		}

		jobName := jobCall.Arguments[0].String()
		jobFn, fnOK := goja.AssertFunction(jobCall.Arguments[1])

		if !fnOK {
			panic(r.jsVM.NewGoError(errors.New("stage.job callback must be a function")))
		}

		scope.addJob(jobName, jobFn)

		return goja.Undefined()
	})

	// Execute the callback synchronously to collect job registrations
	_, err := callback(goja.Undefined(), stageObj)
	if err != nil {
		return r.rejectImmediate(fmt.Errorf("pipeline.stage %q callback failed: %w", stageName, err))
	}

	if len(scope.jobs) == 0 {
		return r.rejectImmediate(fmt.Errorf("pipeline.stage %q has no jobs", stageName))
	}

	// Run all jobs in parallel, wait for all to complete
	return r.jsVM.ToValue(asyncTask(r, "pipeline.stage."+stageName, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, p.runStageJobs(ctx, stageName, scope.jobs)
	}, func(_ struct{}) (any, error) {
		return goja.Undefined(), nil
	}))
}

// runStageJobs runs all jobs in a stage concurrently, collecting results via channels.
func (p *PipelineNamespace) runStageJobs(ctx context.Context, stageName string, jobs []stageJob) error {
	r := p.rt

	type jobResult struct {
		name string
		err  error
	}

	results := make(chan jobResult, len(jobs))

	for _, job := range jobs {
		job := job

		go func() {
			// Execute the JS function on the main thread via tasks channel
			errCh := make(chan error, 1)

			r.promises.Add(1)
			r.tasks <- func() error {
				defer r.promises.Done()

				val, callErr := job.fn(goja.Undefined())
				if callErr != nil {
					errCh <- callErr
					return nil
				}

				// If the job returned a promise, we need to wait for it
				if val != nil && !goja.IsUndefined(val) && !goja.IsNull(val) {
					if promise, isProm := val.Export().(*goja.Promise); isProm {
						// Wait for the promise in a polling fashion on the task queue
						p.waitForPromise(promise, errCh, stageName, job.name)
						return nil
					}
				}

				errCh <- nil

				return nil
			}

			err := <-errCh

			select {
			case results <- jobResult{name: job.name, err: err}:
			case <-ctx.Done():
				results <- jobResult{name: job.name, err: ctx.Err()}
			}
		}()
	}

	var firstErr error

	for range jobs {
		result := <-results
		if result.err != nil && firstErr == nil {
			firstErr = fmt.Errorf("stage %q job %q failed: %w", stageName, result.name, result.err)
		}
	}

	return firstErr
}

// waitForPromise polls a JS promise via the task queue until it resolves or rejects.
func (p *PipelineNamespace) waitForPromise(promise *goja.Promise, errCh chan<- error, stageName, jobName string) {
	r := p.rt

	var check func()
	check = func() {
		switch promise.State() {
		case goja.PromiseStatePending:
			// Re-enqueue check
			r.promises.Add(1)
			r.tasks <- func() error {
				defer r.promises.Done()
				check()

				return nil
			}
		case goja.PromiseStateFulfilled:
			errCh <- nil
		case goja.PromiseStateRejected:
			errCh <- fmt.Errorf("stage %q job %q promise rejected: %v", stageName, jobName, promise.Result())
		}
	}

	check()
}

// gateOptions holds parsed options from the JS gate call.
type gateOptions struct {
	Message string `json:"message"`
	Timeout string `json:"timeout"`
}

// Gate creates an approval gate that pauses pipeline execution until approved/rejected.
// Usage: await pipeline.gate("approve-deploy", { message: "Deploy?", timeout: "4h" })
func (p *PipelineNamespace) Gate(call goja.FunctionCall) goja.Value {
	r := p.rt

	if len(call.Arguments) < 1 {
		return r.rejectImmediate(errors.New("pipeline.gate requires (name[, options])"))
	}

	gateName := call.Arguments[0].String()

	var opts gateOptions
	if len(call.Arguments) >= 2 && !goja.IsUndefined(call.Arguments[1]) && !goja.IsNull(call.Arguments[1]) {
		if err := r.jsVM.ExportTo(call.Arguments[1], &opts); err != nil {
			return r.rejectImmediate(fmt.Errorf("pipeline.gate invalid options: %w", err))
		}
	}

	if r.storage == nil {
		return r.rejectImmediate(errors.New("pipeline.gate requires storage"))
	}

	gateID := uuid.New().String()
	gate := &storage.Gate{
		ID:         gateID,
		RunID:      r.runID,
		PipelineID: r.pipelineID,
		Name:       gateName,
		Status:     storage.GateStatusPending,
		Message:    opts.Message,
	}

	return r.jsVM.ToValue(asyncTask(r, "pipeline.gate."+gateName, func(ctx context.Context) (struct{}, error) {
		if err := r.storage.SaveGate(ctx, gate); err != nil {
			return struct{}{}, fmt.Errorf("pipeline.gate %q: failed to save: %w", gateName, err)
		}

		return struct{}{}, p.pollGate(ctx, gateID, gateName, opts.Timeout)
	}, func(_ struct{}) (any, error) {
		return goja.Undefined(), nil
	}))
}

const gatePollInterval = 2 * time.Second

// pollGate polls storage for gate resolution until approved, rejected, or timed out.
func (p *PipelineNamespace) pollGate(ctx context.Context, gateID, gateName, timeout string) error {
	r := p.rt

	var deadline time.Time
	if timeout != "" {
		dur, err := time.ParseDuration(timeout)
		if err != nil {
			return fmt.Errorf("pipeline.gate %q: invalid timeout %q: %w", gateName, timeout, err)
		}

		deadline = time.Now().Add(dur)
	}

	for {
		if !deadline.IsZero() && time.Now().After(deadline) {
			// Mark gate as timed out
			_ = r.storage.ResolveGate(ctx, gateID, storage.GateStatusTimedOut, "timeout")

			return fmt.Errorf("pipeline.gate %q: timed out", gateName)
		}

		gate, err := r.storage.GetGate(ctx, gateID)
		if err != nil {
			return fmt.Errorf("pipeline.gate %q: poll failed: %w", gateName, err)
		}

		switch gate.Status {
		case storage.GateStatusApproved:
			return nil
		case storage.GateStatusRejected:
			return fmt.Errorf("pipeline.gate %q: rejected by %s", gateName, gate.ApprovedBy)
		case storage.GateStatusTimedOut:
			return fmt.Errorf("pipeline.gate %q: timed out", gateName)
		case storage.GateStatusPending:
			// Continue polling
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(gatePollInterval):
		}
	}
}
