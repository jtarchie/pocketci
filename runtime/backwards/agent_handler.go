package backwards

import (
	"fmt"
	"sync"
	"time"

	"github.com/goccy/go-yaml"
	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/runtime/agent"
	pipelinerunner "github.com/jtarchie/pocketci/runtime/runner"
	"github.com/jtarchie/pocketci/storage"
)

// AgentHandler executes agent steps by delegating to the LLM agent runtime.
type AgentHandler struct{}

// agentRunState holds mutex-protected state accumulated during agent execution.
type agentRunState struct {
	mu                sync.Mutex
	accumulatedOutput string
	latestUsage       *agent.AgentUsage
	auditLog          []agent.AuditEvent
}

// agentRunContext holds the resolved resources needed to run an agent step.
type agentRunContext struct {
	tools            []agent.ToolDef
	mounts           map[string]pipelinerunner.VolumeResult
	outputVolumePath string
	storageKey       string
	hooksStorageKey  string
	auditBaseKey     string
}

func (h *AgentHandler) Execute(sc *StepContext, step *config.Step, pathPrefix string) error {
	agentStep, err := h.loadAgentStep(sc, step, pathPrefix)
	if err != nil {
		return err
	}

	sc.appendExecutedTask(agentStep.Agent)

	arc, err := h.prepareAgentRun(sc, agentStep, pathPrefix)
	if err != nil {
		return &TaskErroredError{TaskName: agentStep.Agent, Err: err}
	}

	state := &agentRunState{}
	startedAt := time.Now()

	_ = sc.Storage.Set(sc.Ctx, arc.storageKey, storage.Payload{
		"status":     "pending",
		"started_at": startedAt.Format(time.RFC3339),
	})

	throttle := newThrottledPersister(500*time.Millisecond, func() {
		state.mu.Lock()
		defer state.mu.Unlock()

		_ = sc.Storage.Set(sc.Ctx, arc.storageKey, storage.Payload{
			"status":     "running",
			"started_at": startedAt.Format(time.RFC3339),
			"stdout":     state.accumulatedOutput,
			"usage":      state.latestUsage,
			"audit_log":  state.auditLog,
		})
	})

	agentConfig := h.buildAgentConfig(sc, agentStep, arc, startedAt, state, throttle)

	result, runErr := agent.RunAgent(sc.Ctx, sc.PipelineRunner, sc.SecretsManager, sc.PipelineID, agentConfig)

	return h.finalizeAgentRun(sc, agentStep, arc, result, runErr, startedAt, state, throttle)
}

// loadAgentStep resolves the final step config by loading and merging any
// external file/URI or inline prompt_file. Returns the original step unchanged
// if no external config is referenced.
func (h *AgentHandler) loadAgentStep(sc *StepContext, step *config.Step, pathPrefix string) (*config.Step, error) {
	if step.File != "" || step.URI != "" {
		merged, err := mergeAgentExternalConfig(sc, step, pathPrefix)
		if err != nil {
			return nil, &TaskErroredError{TaskName: step.Agent, Err: err}
		}

		return merged, nil
	}

	if step.PromptFile != "" {
		contents, err := loadRawBytesFromVolume(sc, step.PromptFile)
		if err != nil {
			return nil, &TaskErroredError{TaskName: step.Agent, Err: fmt.Errorf("loading prompt_file %q: %w", step.PromptFile, err)}
		}

		agentStep := copyStep(step)
		agentStep.Prompt = string(contents)

		return agentStep, nil
	}

	return step, nil
}

// prepareAgentRun resolves tools, mounts, and storage keys for the agent step.
func (h *AgentHandler) prepareAgentRun(sc *StepContext, agentStep *config.Step, pathPrefix string) (agentRunContext, error) {
	tools, err := resolveAgentTools(sc, agentStep, pathPrefix)
	if err != nil {
		return agentRunContext{}, fmt.Errorf("resolving tools: %w", err)
	}

	mounts, outputVolumePath, err := resolveAgentMounts(sc, agentStep)
	if err != nil {
		return agentRunContext{}, fmt.Errorf("resolving mounts: %w", err)
	}

	hooksStorageKey := fmt.Sprintf("%s/%s", sc.BaseStorageKey(), pathPrefix)

	return agentRunContext{
		tools:            tools,
		mounts:           mounts,
		outputVolumePath: outputVolumePath,
		hooksStorageKey:  hooksStorageKey,
		storageKey:       hooksStorageKey + "/run",
		auditBaseKey:     fmt.Sprintf("/agent-audit/%s/jobs/%s/%s/events", sc.RunID, sc.JobName, pathPrefix),
	}, nil
}

// buildAgentConfig constructs the agent.AgentConfig with all callbacks wired up.
func (h *AgentHandler) buildAgentConfig(sc *StepContext, agentStep *config.Step, arc agentRunContext, startedAt time.Time, state *agentRunState, throttle *throttledPersister) agent.AgentConfig {
	image := resolveAgentImage(agentStep)

	cfg := agent.AgentConfig{
		Name:             agentStep.Agent,
		Prompt:           agentStep.Prompt,
		Model:            agentStep.Model,
		Image:            image,
		Mounts:           arc.mounts,
		OutputVolumePath: arc.outputVolumePath,
		LLM:              agentStep.AgentLLM,
		Thinking:         agentStep.AgentThinking,
		Safety:           agentStep.AgentSafety,
		ContextGuard:     agentStep.AgentContextGuard,
		Limits:           agentStep.AgentLimits,
		Context:          agentStep.AgentContext,
		Validation:       agentStep.AgentValidation,
		Memory:           agentStep.AgentMemory,
		OutputSchema:     agentStep.AgentOutputSchema,
		ToolTimeout:      agentStep.AgentToolTimeout,
		Storage:          sc.Storage,
		Namespace:        sc.JobName,
		RunID:            sc.RunID,
		PipelineID:       sc.PipelineID,
		BaseURLOverrides: sc.AgentBaseURLs,
		OnUsage: func(usage agent.AgentUsage) {
			state.mu.Lock()
			state.latestUsage = &usage
			state.mu.Unlock()

			throttle.trigger()
		},
		OnAuditEvent: func(event agent.AuditEvent) {
			state.mu.Lock()
			state.auditLog = append(state.auditLog, event)
			idx := len(state.auditLog) - 1
			state.mu.Unlock()

			_ = sc.Storage.Set(sc.Ctx, fmt.Sprintf("%s/%d", arc.auditBaseKey, idx), storage.Payload{
				"timestamp":    event.Timestamp,
				"invocationId": event.InvocationID,
				"author":       event.Author,
				"type":         event.Type,
				"text":         event.Text,
				"toolName":     event.ToolName,
				"toolCallId":   event.ToolCallID,
				"toolArgs":     event.ToolArgs,
				"toolResult":   event.ToolResult,
				"usage":        event.Usage,
				"index":        idx,
			})

			throttle.trigger()
		},
		OnOutput: func(_, data string) {
			state.mu.Lock()
			state.accumulatedOutput += data
			state.mu.Unlock()

			throttle.trigger()
		},
	}

	if len(arc.tools) > 0 {
		cfg.Tools = arc.tools
	}

	return cfg
}

// finalizeAgentRun writes the final storage status and returns the step error.
func (h *AgentHandler) finalizeAgentRun(sc *StepContext, agentStep *config.Step, arc agentRunContext, result *agent.AgentResult, runErr error, startedAt time.Time, state *agentRunState, throttle *throttledPersister) error {
	elapsed := time.Since(startedAt)

	if runErr != nil {
		state.mu.Lock()
		output := state.accumulatedOutput
		usage := state.latestUsage
		log := state.auditLog
		state.mu.Unlock()

		_ = sc.Storage.Set(sc.Ctx, arc.storageKey, storage.Payload{
			"status":        "failure",
			"started_at":    startedAt.Format(time.RFC3339),
			"elapsed":       elapsed.String(),
			"stdout":        output,
			"error_message": runErr.Error(),
			"usage":         usage,
			"audit_log":     log,
		})

		return &TaskFailedError{TaskName: agentStep.Agent, Code: 1}
	}

	throttle.flush()

	state.mu.Lock()
	usage := state.latestUsage
	if usage == nil && result != nil {
		usage = &result.Usage
	}
	state.mu.Unlock()

	status := "success"
	if result.Status == "limit_exceeded" {
		status = "limit_exceeded"
	}

	_ = sc.Storage.Set(sc.Ctx, arc.storageKey, storage.Payload{
		"status":     status,
		"started_at": startedAt.Format(time.RFC3339),
		"elapsed":    elapsed.String(),
		"stdout":     result.Text,
		"usage":      usage,
		"audit_log":  result.AuditLog,
	})

	return nil
}

// mergeAgentExternalConfig loads a YAML config from the step's file or URI
// and merges it with inline step fields. Returns the step unchanged if
// neither field is set.
func mergeAgentExternalConfig(sc *StepContext, step *config.Step, pathPrefix string) (*config.Step, error) {
	contents, err := loadAgentConfig(sc, step, pathPrefix)
	if err != nil {
		return nil, err
	}

	if contents == nil {
		return step, nil
	}

	return mergeAgentFromContents(contents, step), nil
}

// loadAgentConfig loads raw YAML bytes from a step's file or URI field.
func loadAgentConfig(sc *StepContext, step *config.Step, pathPrefix string) ([]byte, error) {
	if step.File != "" {
		return loadRawBytesFromVolume(sc, step.File)
	}

	if step.URI != "" {
		scheme, value, err := parseURI(step.URI)
		if err != nil {
			return nil, err
		}

		if scheme == schemeFile {
			return loadRawBytesFromVolume(sc, value)
		}

		return loadTaskConfigFromHTTP(sc.Ctx, value)
	}

	return nil, nil
}

// mergeAgentFromContents parses YAML contents and merges with inline step
// fields. Inline values override loaded values (shallow merge).
// Prompts are concatenated so loaded and inline prompts are both included.
func mergeAgentFromContents(contents []byte, inlineStep *config.Step) *config.Step {
	var fileFields map[string]interface{}
	err := yaml.Unmarshal(contents, &fileFields)
	if err != nil {
		return inlineStep
	}

	merged := copyStep(inlineStep)

	// Inherit fields from file if not set inline.
	if merged.Prompt == "" {
		if p, ok := fileFields["prompt"].(string); ok {
			merged.Prompt = p
		}
	} else if p, ok := fileFields["prompt"].(string); ok {
		// Concatenate: file prompt + inline prompt.
		merged.Prompt = p + "\n" + merged.Prompt
	}

	if merged.Model == "" {
		if m, ok := fileFields["model"].(string); ok {
			merged.Model = m
		}
	}

	if merged.TaskConfig == nil {
		if _, ok := fileFields["config"]; ok {
			// Re-unmarshal the full contents to get a typed config.
			var full struct {
				Config *config.TaskConfig `yaml:"config,omitempty"`
			}

			err := yaml.Unmarshal(contents, &full)
			if err == nil && full.Config != nil {
				merged.TaskConfig = full.Config
			}
		}
	}

	if merged.AgentContext == nil {
		if _, ok := fileFields["context"]; ok {
			var full struct {
				Context *agent.AgentContext `yaml:"context,omitempty"`
			}

			err := yaml.Unmarshal(contents, &full)
			if err == nil && full.Context != nil {
				merged.AgentContext = full.Context
			}
		}
	}

	return merged
}

// resolveAgentImage returns the container image for the agent step.
func resolveAgentImage(step *config.Step) string {
	if step.TaskConfig != nil {
		if step.TaskConfig.Image != "" {
			return step.TaskConfig.Image
		}

		if step.TaskConfig.ImageResource.Source != nil {
			if repo, ok := step.TaskConfig.ImageResource.Source["repository"].(string); ok {
				return repo
			}
		}
	}

	return "busybox"
}

// resolveAgentTools builds tool definitions from the step's tools array.
func resolveAgentTools(sc *StepContext, step *config.Step, pathPrefix string) ([]agent.ToolDef, error) {
	if len(step.Tools) == 0 {
		return nil, nil
	}

	storageKeyPrefix := fmt.Sprintf("%s/%s", sc.BaseStorageKey(), pathPrefix)

	var tools []agent.ToolDef

	for i := range step.Tools {
		tool := &step.Tools[i]

		switch {
		case tool.Agent != "":
			def, err := resolveAgentToolDef(sc, tool, pathPrefix, storageKeyPrefix)
			if err != nil {
				return nil, err
			}

			tools = append(tools, def)
		case tool.Task != "":
			def, err := resolveTaskToolDef(sc, tool, pathPrefix, storageKeyPrefix)
			if err != nil {
				return nil, err
			}

			tools = append(tools, def)
		}
	}

	return tools, nil
}

func resolveAgentToolDef(sc *StepContext, tool *config.Step, pathPrefix, storageKeyPrefix string) (agent.ToolDef, error) {
	subStep, err := mergeAgentExternalConfig(sc, tool, pathPrefix)
	if err != nil {
		return agent.ToolDef{}, fmt.Errorf("resolving agent tool %q: %w", tool.Agent, err)
	}

	subImage := resolveAgentImage(subStep)
	if subImage == "busybox" {
		subImage = ""
	}

	return agent.ToolDef{
		Name:             subStep.Agent,
		Prompt:           subStep.Prompt,
		Model:            subStep.Model,
		Image:            subImage,
		StorageKeyPrefix: storageKeyPrefix,
	}, nil
}

func mergeTaskConfigFromFile(sc *StepContext, tool *config.Step, pathPrefix string, taskConfig *config.TaskConfig) *config.TaskConfig {
	if tool.File == "" && tool.URI == "" {
		return taskConfig
	}

	contents, err := loadAgentConfig(sc, tool, pathPrefix)
	if err != nil || contents == nil {
		return taskConfig
	}

	var fileCfg config.TaskConfig
	unmarshalErr := yaml.Unmarshal(contents, &fileCfg)
	if unmarshalErr != nil {
		return taskConfig
	}

	if taskConfig == nil {
		return &fileCfg
	}

	if taskConfig.Run == nil {
		taskConfig.Run = fileCfg.Run
	}

	if taskConfig.Image == "" {
		taskConfig.Image = fileCfg.Image
	}

	return taskConfig
}

func resolveTaskToolDef(sc *StepContext, tool *config.Step, pathPrefix, storageKeyPrefix string) (agent.ToolDef, error) {
	taskConfig := mergeTaskConfigFromFile(sc, tool, pathPrefix, tool.TaskConfig)

	toolImage := ""
	var cmdPath string
	var cmdArgs []string
	var env map[string]string

	if taskConfig != nil {
		toolImage = resolveAgentImage(&config.Step{TaskConfig: taskConfig})
		if toolImage == "busybox" {
			toolImage = ""
		}

		if taskConfig.Run != nil {
			cmdPath = taskConfig.Run.Path
			cmdArgs = taskConfig.Run.Args
		}

		env = taskConfig.Env
	}

	return agent.ToolDef{
		Name:             tool.Task,
		IsTask:           true,
		Description:      tool.Description,
		Image:            toolImage,
		CommandPath:      cmdPath,
		CommandArgs:      cmdArgs,
		Env:              env,
		StorageKeyPrefix: storageKeyPrefix,
	}, nil
}

// resolveAgentMounts builds the mounts map for an agent step.
// It collects declared inputs, auto-mounts context.tasks volumes,
// and auto-creates output volumes. Returns the mounts map and the
// output volume path (first output name).
func resolveAgentMounts(sc *StepContext, step *config.Step) (map[string]pipelinerunner.VolumeResult, string, error) {
	mounts := make(map[string]pipelinerunner.VolumeResult)

	// Collect declared input mounts.
	if step.TaskConfig != nil {
		for _, input := range step.TaskConfig.Inputs {
			if volName, ok := sc.KnownVolumes[input.Name]; ok {
				mounts[input.Name] = pipelinerunner.VolumeResult{Name: volName}
			}
		}
	}

	// Auto-mount volumes for agents referenced in context.tasks.
	if step.AgentContext != nil {
		for _, ct := range step.AgentContext.Tasks {
			if _, alreadyMounted := mounts[ct.Name]; alreadyMounted {
				continue
			}

			if volName, ok := sc.KnownVolumes[ct.Name]; ok {
				mounts[ct.Name] = pipelinerunner.VolumeResult{Name: volName}
			}
		}
	}

	// Determine outputs: explicit or auto-create one named after the agent.
	var outputs []config.Output

	if step.TaskConfig != nil && len(step.TaskConfig.Outputs) > 0 {
		outputs = step.TaskConfig.Outputs
	} else {
		outputs = []config.Output{{Name: step.Agent}}
	}

	for _, output := range outputs {
		volName, ok := sc.KnownVolumes[output.Name]
		if !ok {
			volName = resourceVolumeName(sc.RunID, output.Name)
			sc.KnownVolumes[output.Name] = volName
		}

		mounts[output.Name] = pipelinerunner.VolumeResult{Name: volName}
	}

	outputVolumePath := ""
	if len(outputs) > 0 {
		outputVolumePath = outputs[0].Name
	}

	return mounts, outputVolumePath, nil
}

// copyStep creates a shallow copy of a Step.
func copyStep(step *config.Step) *config.Step {
	copied := *step

	return &copied
}

// throttledPersister implements a simple time-based throttle for storage writes.
// Callbacks are invoked synchronously from the agent runtime, so no goroutine is needed.
type throttledPersister struct {
	mu            sync.Mutex
	lastPersistAt time.Time
	pending       bool
	interval      time.Duration
	fn            func()
}

func newThrottledPersister(interval time.Duration, fn func()) *throttledPersister {
	return &throttledPersister{
		interval: interval,
		fn:       fn,
	}
}

func (tp *throttledPersister) trigger() {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	if time.Since(tp.lastPersistAt) < tp.interval {
		tp.pending = true

		return
	}

	tp.doPersist()
}

func (tp *throttledPersister) flush() {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	if tp.pending {
		tp.doPersist()
	}
}

func (tp *throttledPersister) doPersist() {
	tp.pending = false
	tp.lastPersistAt = time.Now()
	tp.fn()
}
