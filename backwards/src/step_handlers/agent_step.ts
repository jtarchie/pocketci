/// <reference path="../../../packages/pocketci/src/global.d.ts" />

import { TaskFailure } from "../task_runner.ts";
import { errorMessage, formatElapsed } from "../utils.ts";
import { loadFileFromVolume, loadFromURI } from "./file_loader.ts";
import { processHooks } from "./resource_helpers.ts";
import type { StepContext } from "./step_context.ts";
import type { StepHandler } from "./step_handler.ts";

// mergeAgentFromContents parses YAML contents and merges with inline step
// fields. Inline values override loaded values (shallow merge).
// Prompts are concatenated so loaded and inline prompts are both included.
function mergeAgentFromContents(
  contents: string,
  inlineStep: AgentStep,
): AgentStep {
  const fileConfig = yaml.parse(contents) as Partial<AgentStep>;
  const merged = {
    ...fileConfig,
    ...inlineStep,
    agent: inlineStep.agent,
  } as AgentStep;
  if (fileConfig.prompt && inlineStep.prompt) {
    merged.prompt = fileConfig.prompt + "\n" + inlineStep.prompt;
  } else if (!inlineStep.prompt && fileConfig.prompt) {
    merged.prompt = fileConfig.prompt;
  }
  if (!inlineStep.model && fileConfig.model) merged.model = fileConfig.model;
  if (!inlineStep.config && fileConfig.config) {
    merged.config = fileConfig.config;
  }
  if (!inlineStep.context && fileConfig.context) {
    merged.context = fileConfig.context;
  }
  return merged;
}

// mergeAgentFileConfig loads a YAML file from a volume and merges it with
// inline step fields.
async function mergeAgentFileConfig(
  ctx: StepContext,
  inlineStep: AgentStep,
  pathContext: string,
): Promise<AgentStep> {
  const contents = await loadFileFromVolume(ctx, inlineStep.file!, pathContext);
  return mergeAgentFromContents(contents, inlineStep);
}

// mergeAgentURIConfig loads a YAML config from a URI and merges it with
// inline step fields.
async function mergeAgentURIConfig(
  ctx: StepContext,
  inlineStep: AgentStep,
  pathContext: string,
): Promise<AgentStep> {
  const contents = await loadFromURI(ctx, inlineStep.uri!, pathContext);
  return mergeAgentFromContents(contents, inlineStep);
}

export class AgentStepHandler implements StepHandler {
  getIdentifier(step: Step): string {
    return `agent/${(step as AgentStep).agent}`;
  }

  async process(
    ctx: StepContext,
    step: AgentStep,
    pathContext: string,
  ): Promise<void> {
    let agentStep = step;

    // Load full agent config from a YAML file on a volume or from a URI.
    // Inline fields on the step override file-loaded values (no deep merge).
    if ("file" in step && step.file) {
      agentStep = await mergeAgentFileConfig(ctx, step, pathContext);
    } else if ("uri" in step && step.uri) {
      agentStep = await mergeAgentURIConfig(ctx, step, pathContext);
    } else if ("prompt_file" in step && step.prompt_file) {
      // Load just the prompt text from a plain text file on a volume.
      const contents = await loadFileFromVolume(
        ctx,
        step.prompt_file,
        pathContext,
      );
      agentStep = { ...step, prompt: contents };
    }

    const hooksStorageKey = `${ctx.paths.getBaseStorageKey()}/${pathContext}`;
    const storageKey = `${hooksStorageKey}/run`;
    const auditBaseKey =
      `/agent-audit/${ctx.buildID}/jobs/${ctx.jobName}/${pathContext}/events`;

    const image = agentStep.config?.image ??
      agentStep.config?.image_resource?.source?.repository ??
      "busybox";

    // Resolve tool definitions (agent tools and task tools) from the unified
    // `tools` array. Each entry is distinguished by field presence:
    //   - `agent:` field → LLM sub-agent tool
    //   - `task:` field → container command tool
    const tools: ToolDef[] = [];
    for (const rawTool of (agentStep.tools ?? [])) {
      if ("agent" in rawTool && rawTool.agent) {
        // Agent tool — resolve from file or uri if needed.
        let subStep = rawTool as AgentStep;
        if ("file" in rawTool && rawTool.file) {
          subStep = await mergeAgentFileConfig(
            ctx,
            rawTool as AgentStep,
            pathContext,
          );
        } else if ("uri" in rawTool && (rawTool as AgentStep).uri) {
          subStep = await mergeAgentURIConfig(
            ctx,
            rawTool as AgentStep,
            pathContext,
          );
        }
        const subImage = subStep.config?.image ??
          subStep.config?.image_resource?.source?.repository ?? "";
        tools.push({
          name: subStep.agent,
          prompt: subStep.prompt ?? "",
          model: subStep.model ?? "",
          image: subImage,
          storageKeyPrefix: storageKey.replace(/\/run$/, ""),
        });
      } else if ("task" in rawTool && rawTool.task) {
        // Task tool — container command exposed as a tool.
        const taskTool = rawTool as TaskToolStep;
        let taskConfig = taskTool.config;
        if ("file" in taskTool && taskTool.file) {
          const contents = await loadFileFromVolume(
            ctx,
            taskTool.file,
            pathContext,
          );
          const fileConfig = yaml.parse(contents) as Partial<TaskConfig>;
          taskConfig = { ...fileConfig, ...taskConfig } as TaskConfig;
        } else if ("uri" in taskTool && taskTool.uri) {
          const contents = await loadFromURI(
            ctx,
            taskTool.uri,
            pathContext,
          );
          const fileConfig = yaml.parse(contents) as Partial<TaskConfig>;
          taskConfig = { ...fileConfig, ...taskConfig } as TaskConfig;
        }
        tools.push({
          name: taskTool.task,
          is_task: true,
          description: taskTool.description ?? "",
          image: taskConfig?.image ??
            taskConfig?.image_resource?.source?.repository ?? "",
          command_path: taskConfig?.run?.path ?? "",
          command_args: taskConfig?.run?.args ?? [],
          env: taskConfig?.env ?? {},
          storageKeyPrefix: storageKey.replace(/\/run$/, ""),
        });
      }
    }

    // Collect input and output mounts from earlier get/put steps and volumes.
    const mounts: KnownMounts = {};
    for (const input of (agentStep.config?.inputs ?? [])) {
      const knownMount = ctx.taskRunner.getKnownMounts()[input.name];
      if (knownMount) {
        mounts[input.name] = knownMount;
      }
    }

    // Auto-mount volumes for agents referenced in context.tasks.
    // This removes the need to duplicate them in config.inputs when the
    // prior agent's output volume was auto-named (Change 1 below).
    for (const ct of (agentStep.context?.tasks ?? [])) {
      const knownMount = ctx.taskRunner.getKnownMounts()[ct.name];
      if (knownMount && !mounts[ct.name]) {
        mounts[ct.name] = knownMount;
      }
    }

    // If no outputs are declared, auto-create one named after the agent.
    // This volume is registered in knownMounts so downstream steps can
    // reference it by the agent's name without explicit wiring.
    const declaredOutputs = agentStep.config?.outputs ?? [];
    const outputs = declaredOutputs.length > 0
      ? declaredOutputs
      : [{ name: agentStep.agent }];
    for (const output of outputs) {
      ctx.taskRunner.getKnownMounts()[output.name] ||= await runtime
        .createVolume({ name: output.name });
      mounts[output.name] = ctx.taskRunner.getKnownMounts()[output.name];
    }

    const outputVolumePath = outputs.length > 0 ? outputs[0].name : "";

    let accumulatedOutput = "";
    let latestUsage: AgentUsage | undefined;
    const auditLog: AuditEvent[] = [];
    const startedAt = new Date().toISOString();

    storage.set(storageKey, { status: "pending", started_at: startedAt });

    // Throttled persistence helpers
    let persistPending = false;
    let lastPersistMs = 0;
    const persistThrottleMs = 500;

    const doPersist = () => {
      persistPending = false;
      lastPersistMs = Date.now();
      storage.set(storageKey, {
        status: "running",
        started_at: startedAt,
        stdout: accumulatedOutput,
        usage: latestUsage,
        audit_log: auditLog,
      });
    };

    const persistRunningState = () => {
      if (Date.now() - lastPersistMs < persistThrottleMs) {
        persistPending = true;
        return;
      }
      doPersist();
    };

    let failure: unknown = undefined;

    try {
      const result = await runtime.agent({
        name: agentStep.agent,
        prompt: agentStep.prompt,
        model: agentStep.model,
        image,
        mounts,
        outputVolumePath,
        llm: agentStep.llm,
        thinking: agentStep.thinking,
        safety: agentStep.safety,
        context_guard: agentStep.context_guard,
        limits: agentStep.limits,
        context: agentStep.context,
        validation: agentStep.validation,
        output_schema: agentStep.output_schema,
        tool_timeout: agentStep.tool_timeout,
        tools: tools.length > 0 ? tools : undefined,
        onUsage: (usage: AgentUsage) => {
          latestUsage = usage;
          persistRunningState();
        },
        onAuditEvent: (event: AuditEvent) => {
          auditLog.push(event);
          storage.set(`${auditBaseKey}/${auditLog.length - 1}`, {
            ...event,
            index: auditLog.length - 1,
          });
          persistRunningState();
        },
        onOutput: (_stream: "stdout" | "stderr", data: string) => {
          accumulatedOutput += data;
          persistRunningState();
        },
      });

      if (persistPending) doPersist();

      storage.set(storageKey, {
        status: result.status === "limit_exceeded"
          ? "limit_exceeded"
          : "success",
        started_at: startedAt,
        elapsed: formatElapsed(startedAt),
        stdout: result.text,
        usage: latestUsage ?? result.usage,
        audit_log: result.auditLog,
      });

      for (const output of outputs) {
        ctx.taskRunner.getKnownMounts()[output.name] = mounts[output.name];
      }
    } catch (error) {
      failure = error;
      const errMsg = errorMessage(error);
      storage.set(storageKey, {
        status: "failure",
        started_at: startedAt,
        elapsed: formatElapsed(startedAt),
        stdout: accumulatedOutput,
        error_message: errMsg,
        usage: latestUsage,
        audit_log: auditLog,
      });
    }

    await processHooks(ctx, step, pathContext, hooksStorageKey, failure);

    if (failure) {
      const errMsg = errorMessage(failure);
      throw new TaskFailure(`Agent ${agentStep.agent} failed: ${errMsg}`);
    }
  }
}
