package backwards

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/template"
	"time"

	"github.com/go-playground/validator/v10"
	sprig "github.com/go-task/slim-sprig/v3"
	"github.com/goccy/go-yaml"
)

// preprocessYAML checks for an opt-in template marker ("pocketci: template" on
// the first line) and renders the YAML using Go text/template with Sprig functions.
// If no marker is found, returns the original content unchanged. Template
// rendering errors are returned as wrapped errors with "pipeline template" prefix.
func preprocessYAML(content []byte) ([]byte, error) {
	contentStr := string(content)

	// Check for opt-in marker on first line
	lines := strings.SplitN(contentStr, "\n", 2)
	if len(lines) == 0 || !strings.Contains(lines[0], "pocketci: template") {
		// No opt-in marker; return unchanged
		return content, nil
	}

	// Marker found; render as template with Sprig functions
	tmpl, err := template.New("pipeline").Funcs(sprig.FuncMap()).Parse(contentStr)
	if err != nil {
		return nil, fmt.Errorf("pipeline template parse failed: %w", err)
	}

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, nil)
	if err != nil {
		return nil, fmt.Errorf("pipeline template render failed: %w", err)
	}

	return buf.Bytes(), nil
}

// ParseConfig unmarshals a Concourse YAML pipeline content into a Config.
func ParseConfig(content string) (*Config, error) {
	var cfg Config
	err := yaml.Unmarshal([]byte(content), &cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to parse pipeline config: %w", err)
	}

	return &cfg, nil
}

// LoadConfig reads a YAML pipeline file from disk, applies Go template
// preprocessing (if opted in), and returns the parsed Config.
func LoadConfig(filename string) (*Config, error) {
	contents, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("could not read pipeline: %w", err)
	}

	processed, err := preprocessYAML(contents)
	if err != nil {
		return nil, err
	}

	var cfg Config
	err = yaml.Unmarshal(processed, &cfg)
	if err != nil {
		return nil, fmt.Errorf("could not unmarshal pipeline: %w", err)
	}

	return &cfg, nil
}

// ValidatePipeline validates that the given YAML content is a well-formed
// pipeline definition without producing any output. It is suitable for early
// error checking at pipeline-set time without performing transpilation.
func ValidatePipeline(content []byte) error {
	var config Config

	// Preprocess YAML templates if opted in
	processed, err := preprocessYAML(content)
	if err != nil {
		return err
	}

	err = yaml.Unmarshal(processed, &config)
	if err != nil {
		return fmt.Errorf("could not unmarshal pipeline: %w", err)
	}

	validate := validator.New(validator.WithRequiredStructEnabled())

	err = validate.Struct(config)
	if err != nil {
		return fmt.Errorf("could not validate pipeline: %w", err)
	}

	err = validateResourceTypes(&config)
	if err != nil {
		return err
	}

	err = validateSteps(config.Jobs)
	if err != nil {
		return err
	}

	err = validateConcurrency(&config)
	if err != nil {
		return err
	}

	err = validateInputOutputWiring(config.Jobs)
	if err != nil {
		return err
	}

	err = validateDependsOn(config.Jobs)
	if err != nil {
		return err
	}

	err = validateGateConfigs(config.Jobs)
	if err != nil {
		return err
	}

	return nil
}

// hasExternalConfig returns true when the step loads config from a file or URI.
func hasExternalConfig(step Step) bool {
	return step.File != "" || step.URI != ""
}

// validateSteps checks that task steps have a required run.path field (unless using file: or uri:).
func validateSteps(jobs Jobs) error {
	for _, job := range jobs {
		for i, step := range job.Plan {
			if step.File != "" && step.URI != "" {
				return fmt.Errorf("step in job %q (index %d) cannot have both file and uri", job.Name, i)
			}

			if step.Task != "" && !hasExternalConfig(step) {
				if step.TaskConfig == nil || step.TaskConfig.Run == nil || step.TaskConfig.Run.Path == "" {
					return fmt.Errorf("task step %q in job %q (index %d) requires config.run.path", step.Task, job.Name, i)
				}
			}

			if step.Agent != "" && !hasExternalConfig(step) && step.Prompt == "" && step.PromptFile == "" {
				return fmt.Errorf("agent step %q in job %q (index %d) requires prompt, prompt_file, file, or uri", step.Agent, job.Name, i)
			}
		}
	}

	return nil
}

func validateConcurrency(config *Config) error {
	if config.MaxInFlight < 0 {
		return errors.New("pipeline max_in_flight must be greater than 0 when set")
	}

	for _, job := range config.Jobs {
		if job.MaxInFlight < 0 {
			return fmt.Errorf("job %q max_in_flight must be greater than 0 when set", job.Name)
		}

		for i, step := range job.Plan {
			err := validateStepConcurrency(job.Name, i, step)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func validateStepConcurrency(jobName string, stepIndex int, step Step) error {
	if step.Parallelism < 0 {
		return fmt.Errorf("job %q step %d parallelism must be greater than 0 when set", jobName, stepIndex)
	}

	if step.Parallelism > 0 && step.Task == "" {
		return fmt.Errorf("job %q step %d parallelism is only valid on task steps", jobName, stepIndex)
	}

	if step.InParallel.Limit < 0 {
		return fmt.Errorf("job %q step %d in_parallel.limit must be greater than 0 when set", jobName, stepIndex)
	}

	for _, acrossVar := range step.Across {
		if acrossVar.MaxInFlight < 0 {
			return fmt.Errorf("job %q step %d across.max_in_flight must be greater than 0 when set", jobName, stepIndex)
		}
	}

	for nestedIndex, nested := range step.Do {
		err := validateStepConcurrency(jobName, nestedIndex, nested)
		if err != nil {
			return err
		}
	}

	for nestedIndex, nested := range step.Try {
		err := validateStepConcurrency(jobName, nestedIndex, nested)
		if err != nil {
			return err
		}
	}

	for nestedIndex, nested := range step.InParallel.Steps {
		err := validateStepConcurrency(jobName, nestedIndex, nested)
		if err != nil {
			return err
		}
	}

	return nil
}

// validateInputOutputWiring checks that every task or agent step's declared
// inputs are satisfied by outputs from earlier steps in the same job. Steps
// that load their config entirely from a file: are skipped because their
// inputs are only known at runtime.
func validateInputOutputWiring(jobs Jobs) error {
	for _, job := range jobs {
		available := make(map[string]bool)

		for _, step := range job.Plan {
			collectStepOutputs(step, available)

			if errs := checkStepInputs(step, available, job.Name); len(errs) > 0 {
				return errs[0]
			}
		}
	}

	return nil
}

// collectStepOutputs registers the outputs a step makes available for
// subsequent steps in the same job.
func collectStepOutputs(step Step, available map[string]bool) {
	switch {
	case step.Get != "":
		available[step.Get] = true
	case step.Put != "":
		available[step.Put] = true
	case step.Task != "":
		if step.TaskConfig != nil {
			for _, out := range step.TaskConfig.Outputs {
				available[out.Name] = true
			}
		}
	case step.Agent != "":
		if step.TaskConfig != nil && len(step.TaskConfig.Outputs) > 0 {
			for _, out := range step.TaskConfig.Outputs {
				available[out.Name] = true
			}
		} else {
			// Agent steps auto-create an output named after the agent.
			available[step.Agent] = true
		}
	}

	// Recurse into composite steps.
	for _, nested := range step.Do {
		collectStepOutputs(nested, available)
	}

	for _, nested := range step.InParallel.Steps {
		collectStepOutputs(nested, available)
	}
}

// checkStepInputs verifies that every declared input on a step exists in the
// available outputs set. Steps using file: without inline config.inputs are
// skipped (inputs unknown until runtime).
func checkStepInputs(step Step, available map[string]bool, jobName string) []error {
	var errs []error

	stepName := step.Task
	if stepName == "" {
		stepName = step.Agent
	}

	// Only validate when we have inline inputs to check.
	// Skip steps that use file: or prompt_file: — their inputs bootstrap
	// volumes for runtime file loading, not prior-step output consumption.
	if stepName != "" && step.TaskConfig != nil && step.File == "" && step.URI == "" && step.PromptFile == "" {
		for _, in := range step.TaskConfig.Inputs {
			if !available[in.Name] {
				errs = append(errs, fmt.Errorf(
					"step %q in job %q declares input %q, but no prior step produces it as an output",
					stepName, jobName, in.Name,
				))
			}
		}
	}

	// Recurse into composite steps.
	for _, nested := range step.Do {
		errs = append(errs, checkStepInputs(nested, available, jobName)...)
	}

	for _, nested := range step.InParallel.Steps {
		errs = append(errs, checkStepInputs(nested, available, jobName)...)
	}

	return errs
}

// validateResourceTypes checks that every resource references a defined resource type.
// The "registry-image" type is built-in and always available.
func validateResourceTypes(config *Config) error {
	// Build a set of valid resource type names
	validTypes := make(map[string]bool)
	validTypes["registry-image"] = true // Built-in type

	for _, rt := range config.ResourceTypes {
		validTypes[rt.Name] = true
	}

	// Check each resource has a valid type
	for _, resource := range config.Resources {
		if !validTypes[resource.Type] {
			return fmt.Errorf("resource %q has undefined resource type %q", resource.Name, resource.Type)
		}
	}

	return nil
}

// validateDependsOn checks that all depends_on references point to existing jobs
// and that the dependency graph has no cycles.
func validateDependsOn(jobs Jobs) error {
	jobNames := make(map[string]bool, len(jobs))
	for _, job := range jobs {
		jobNames[job.Name] = true
	}

	// Check all references are valid
	for _, job := range jobs {
		for _, dep := range job.DependsOn {
			if !jobNames[dep] {
				return fmt.Errorf("job %q depends_on unknown job %q", job.Name, dep)
			}

			if dep == job.Name {
				return fmt.Errorf("job %q depends_on itself", job.Name)
			}
		}
	}

	// Check for cycles using DFS
	adj := make(map[string][]string, len(jobs))
	for _, job := range jobs {
		adj[job.Name] = job.DependsOn
	}

	const (
		white = 0 // unvisited
		gray  = 1 // in current path
		black = 2 // fully explored
	)

	color := make(map[string]int, len(jobs))

	var visit func(string) error
	visit = func(name string) error {
		color[name] = gray

		for _, dep := range adj[name] {
			switch color[dep] {
			case gray:
				return fmt.Errorf("circular dependency detected: %s -> %s", name, dep)
			case white:
				err := visit(dep)
				if err != nil {
					return err
				}
			}
		}

		color[name] = black

		return nil
	}

	for _, job := range jobs {
		if color[job.Name] == white {
			err := visit(job.Name)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// validateGateConfigs checks that gate timeout values are valid Go durations when specified.
func validateGateConfigs(jobs Jobs) error {
	for _, job := range jobs {
		if job.Gate == nil {
			continue
		}

		if job.Gate.Timeout != "" {
			_, durErr := time.ParseDuration(job.Gate.Timeout)
			if durErr != nil {
				return fmt.Errorf("job %q gate timeout %q is not a valid duration: %w", job.Name, job.Gate.Timeout, durErr)
			}
		}
	}

	return nil
}
