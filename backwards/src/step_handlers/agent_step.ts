/// <reference path="../../../packages/pocketci/src/global.d.ts" />

import { TaskFailure } from "../task_runner.ts";
import { errorMessage, formatElapsed } from "../utils.ts";
import { loadFileFromVolume } from "./file_loader.ts";
import type { StepContext } from "./step_context.ts";
import type { StepHandler } from "./step_handler.ts";

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

    // Load full agent config from a YAML file on a volume.
    // Inline fields on the step override file-loaded values (no deep merge).
    if ("file" in step && step.file) {
      const contents = await loadFileFromVolume(ctx, step.file, pathContext);
      const fileConfig = YAML.parse(contents) as Partial<AgentStep>;
      agentStep = {
        ...fileConfig,
        ...step,
        agent: step.agent,
      } as AgentStep;
      // Concatenate file prompt + inline prompt so shared instructions
      // and step-specific prompts are both included.
      if (fileConfig.prompt && step.prompt) {
        agentStep.prompt = fileConfig.prompt + "\n" + step.prompt;
      } else if (!step.prompt && fileConfig.prompt) {
        agentStep.prompt = fileConfig.prompt;
      }
      if (!step.model && fileConfig.model) agentStep.model = fileConfig.model;
      if (!step.config && fileConfig.config) {
        agentStep.config = fileConfig.config;
      }
      if (!step.context && fileConfig.context) {
        agentStep.context = fileConfig.context;
      }
    } else if ("prompt_file" in step && step.prompt_file) {
      // Load just the prompt text from a plain text file on a volume.
      const contents = await loadFileFromVolume(
        ctx,
        step.prompt_file,
        pathContext,
      );
      agentStep = { ...step, prompt: contents };
    }

    const storageKey = `${ctx.paths.getBaseStorageKey()}/${pathContext}/run`;
    const auditBaseKey =
      `/agent-audit/${ctx.buildID}/jobs/${ctx.jobName}/${pathContext}/events`;

    const image = agentStep.config?.image ??
      agentStep.config?.image_resource?.source?.repository ??
      "busybox";

    // Resolve sub-agent configs (loading from file if needed).
    // Each sub-agent is passed to the Go runtime as a SubAgentConfig so the
    // parent LLM can call them as tools.
    const subAgents: SubAgentConfig[] = [];
    for (const rawSub of (agentStep.sub_agents ?? [])) {
      let subStep = rawSub;
      if ("file" in rawSub && rawSub.file) {
        const contents = await loadFileFromVolume(
          ctx,
          rawSub.file,
          pathContext,
        );
        const fileConfig = YAML.parse(contents) as Partial<AgentStep>;
        subStep = {
          ...fileConfig,
          ...rawSub,
          agent: rawSub.agent,
        } as AgentStep;
        // Concatenate file prompt + inline prompt so shared rules and
        // per-agent task descriptions are both included.
        if (fileConfig.prompt && rawSub.prompt) {
          subStep.prompt = fileConfig.prompt + "\n" + rawSub.prompt;
        } else if (!rawSub.prompt && fileConfig.prompt) {
          subStep.prompt = fileConfig.prompt;
        }
        if (!rawSub.model && fileConfig.model) subStep.model = fileConfig.model;
      }
      const subImage = subStep.config?.image ??
        subStep.config?.image_resource?.source?.repository ?? "";
      subAgents.push({
        name: subStep.agent,
        prompt: subStep.prompt ?? "",
        model: subStep.model ?? "",
        image: subImage,
        storageKeyPrefix: storageKey.replace(/\/run$/, ""),
      });
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
        sub_agents: subAgents.length > 0 ? subAgents : undefined,
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
      throw new TaskFailure(`Agent ${agentStep.agent} failed: ${errMsg}`);
    }
  }
}
