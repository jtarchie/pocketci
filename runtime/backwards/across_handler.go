package backwards

import (
	"context"
	"fmt"
	"strings"

	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/storage"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

// combination holds a single Cartesian-product result with a shared key slice
// and a per-combination value slice. Keys is shared across all combinations to
// avoid repeating the allocation for every entry.
type combination struct {
	Keys   []string
	Values []string
}

func (c combination) get(key string) string {
	for i, k := range c.Keys {
		if k == key {
			return c.Values[i]
		}
	}

	return ""
}

// generateCombinations returns the cartesian product of all AcrossVar values.
// It uses a flat []combination representation instead of []map[string]string
// to reduce heap allocations: one shared Keys slice + one Values slice per combo
// instead of one full map per combo.
func generateCombinations(vars []config.AcrossVar) []combination {
	if len(vars) == 0 {
		return []combination{{Keys: []string{}, Values: []string{}}}
	}

	total := 1
	for _, v := range vars {
		total *= len(v.Values)
	}

	keys := make([]string, len(vars))
	for i, v := range vars {
		keys[i] = v.Var
	}

	combos := make([]combination, total)

	for i := range combos {
		vals := make([]string, len(vars))
		idx := i

		for j := len(vars) - 1; j >= 0; j-- {
			size := len(vars[j].Values)
			vals[j] = vars[j].Values[idx%size]
			idx /= size
		}

		combos[i] = combination{Keys: keys, Values: vals}
	}

	return combos
}

// expandAcrossStep clones the step with across variables injected.
// It removes Across/AcrossFailFast, appends variable values to Task name,
// and injects variables into TaskConfig.Env.
func expandAcrossStep(step *config.Step, combo combination, acrossVars []config.AcrossVar) config.Step {
	cloned := *step
	cloned.Across = nil
	cloned.AcrossFailFast = false

	if cloned.Task != "" {
		parts := make([]string, 0, len(acrossVars))
		for _, av := range acrossVars {
			parts = append(parts, combo.get(av.Var))
		}

		cloned.Task = fmt.Sprintf("%s-%s", step.Task, strings.Join(parts, "-"))
	}

	if step.TaskConfig != nil {
		newConfig := *step.TaskConfig
		newConfig.Env = cloneEnv(step.TaskConfig.Env)

		for i, k := range combo.Keys {
			newConfig.Env[k] = combo.Values[i]
		}

		cloned.TaskConfig = &newConfig
	}

	return cloned
}

// resolveAcrossLimit computes the effective concurrency limit for across expansion.
// Takes the minimum of all per-var max_in_flight values, defaulting to 1 (sequential).
func resolveAcrossLimit(vars []config.AcrossVar) int {
	minLimit := 0

	for _, v := range vars {
		if v.MaxInFlight > 0 {
			if minLimit == 0 || v.MaxInFlight < minLimit {
				minLimit = v.MaxInFlight
			}
		}
	}

	if minLimit == 0 {
		return 1
	}

	return minLimit
}

// executeAcross handles a step with across variables by expanding combinations
// and processing each one with concurrency control and fail-fast semantics.
func executeAcross(
	sc *StepContext,
	step *config.Step,
	pathPrefix string,
	processStep func(*config.Step, string) error,
) error {
	storageKey := fmt.Sprintf("%s/%s/across", sc.BaseStorageKey(), pathPrefix)

	err := sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
		"status": "pending",
	})
	if err != nil {
		return fmt.Errorf("storage set pending: %w", err)
	}

	combinations := generateCombinations(step.Across)
	failFast := step.AcrossFailFast

	limit := resolveAcrossLimit(step.Across)
	if failFast {
		limit = 1
	}

	var (
		g      *errgroup.Group
		gCtx   = sc.Ctx
		cancel context.CancelFunc = func() {}
	)

	if failFast {
		gCtx, cancel = context.WithCancel(sc.Ctx)
		g = &errgroup.Group{}
	} else {
		g = &errgroup.Group{}
	}

	defer cancel()

	sem := semaphore.NewWeighted(int64(limit))

	for i, combo := range combinations {
		if err := sem.Acquire(gCtx, 1); err != nil {
			break // gCtx cancelled (fail-fast triggered by a prior goroutine error)
		}

		idx := i
		c := combo

		g.Go(func() error {
			expanded := expandAcrossStep(step, c, step.Across)

			varParts := make([]string, 0, len(step.Across))
			for _, av := range step.Across {
				varParts = append(varParts, fmt.Sprintf("%s_%s", av.Var, c.get(av.Var)))
			}

			varContext := strings.Join(varParts, "_")
			innerPrefix := fmt.Sprintf("%s/across/%d_%s", pathPrefix, idx, varContext)

			err := processStep(&expanded, innerPrefix)
			if err != nil {
				cancel() // cancel context BEFORE releasing semaphore to prevent race
			}
			sem.Release(1)
			return err
		})
	}

	runErr := g.Wait()

	err = sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
		"status": statusFromErr(runErr),
	})
	if err != nil {
		return fmt.Errorf("storage set result: %w", err)
	}

	return runErr
}
