package backwards

import (
	"fmt"
	"strings"

	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/storage"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

// generateCombinations returns the cartesian product of all AcrossVar values.
// Each combination is a map from variable name to value.
func generateCombinations(vars []config.AcrossVar) []map[string]string {
	if len(vars) == 0 {
		return []map[string]string{{}}
	}

	first := vars[0]
	rest := generateCombinations(vars[1:])
	combinations := make([]map[string]string, 0, len(first.Values)*len(rest))

	for _, value := range first.Values {
		for _, restCombo := range rest {
			combo := make(map[string]string, len(restCombo)+1)
			combo[first.Var] = value

			for k, v := range restCombo {
				combo[k] = v
			}

			combinations = append(combinations, combo)
		}
	}

	return combinations
}

// expandAcrossStep clones the step with across variables injected.
// It removes Across/AcrossFailFast, appends variable values to Task name,
// and injects variables into TaskConfig.Env.
func expandAcrossStep(step *config.Step, combination map[string]string, acrossVars []config.AcrossVar) config.Step {
	cloned := *step
	cloned.Across = nil
	cloned.AcrossFailFast = false

	if cloned.Task != "" {
		parts := make([]string, 0, len(acrossVars))
		for _, av := range acrossVars {
			parts = append(parts, combination[av.Var])
		}

		cloned.Task = fmt.Sprintf("%s-%s", step.Task, strings.Join(parts, "-"))
	}

	if step.TaskConfig != nil {
		newConfig := *step.TaskConfig
		newConfig.Env = cloneEnv(step.TaskConfig.Env)

		for k, v := range combination {
			newConfig.Env[k] = v
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
		g    *errgroup.Group
		gCtx = sc.Ctx
	)

	if failFast {
		g, gCtx = errgroup.WithContext(sc.Ctx)
	} else {
		g = &errgroup.Group{}
	}

	sem := semaphore.NewWeighted(int64(limit))

	for i, combination := range combinations {
		if err := sem.Acquire(gCtx, 1); err != nil {
			break // gCtx cancelled (fail-fast triggered by a prior goroutine error)
		}

		idx := i
		combo := combination

		g.Go(func() error {
			defer sem.Release(1)

			expanded := expandAcrossStep(step, combo, step.Across)

			varParts := make([]string, 0, len(step.Across))
			for _, av := range step.Across {
				varParts = append(varParts, fmt.Sprintf("%s_%s", av.Var, combo[av.Var]))
			}

			varContext := strings.Join(varParts, "_")
			innerPrefix := fmt.Sprintf("%s/across/%d_%s", pathPrefix, idx, varContext)

			return processStep(&expanded, innerPrefix)
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
