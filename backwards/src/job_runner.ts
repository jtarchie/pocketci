/// <reference path="../../packages/pocketci/src/global.d.ts" />

import {
  TaskAbort,
  TaskErrored,
  TaskFailure,
  TaskRunner,
} from "./task_runner.ts";
import { JobConcurrency } from "./job_concurrency.ts";
import { JobStoragePaths, zeroPadWithLength } from "./job_storage_paths.ts";
import { StepVariableResolver } from "./step_variable_resolver.ts";
import {
  errorMessage,
  extractJobDependencies,
  failureHook,
  failureStatus,
  getBuildID,
} from "./utils.ts";
import type { StepContext } from "./step_handlers/step_context.ts";
import type { StepHandler } from "./step_handlers/step_handler.ts";
import { AcrossStepHandler } from "./step_handlers/across_step.ts";
import { AgentStepHandler } from "./step_handlers/agent_step.ts";
import { DoStepHandler } from "./step_handlers/do_step.ts";
import { GetStepHandler } from "./step_handlers/get_step.ts";
import { NotifyStepHandler } from "./step_handlers/notify_step.ts";
import { PutStepHandler } from "./step_handlers/put_step.ts";
import { TaskStepHandler } from "./step_handlers/task_step.ts";
import { TryStepHandler } from "./step_handlers/try_step.ts";

const buildID = getBuildID();

export class JobRunner {
  private taskNames: string[] = [];
  private taskRunner: TaskRunner;
  private buildID: string;
  private paths: JobStoragePaths;
  private concurrency: JobConcurrency;
  private variableResolver: StepVariableResolver;
  private ctx: StepContext;

  private doHandler = new DoStepHandler();
  private acrossHandler = new AcrossStepHandler();
  private handlers: [string, StepHandler][] = [
    ["get", new GetStepHandler()],
    ["do", this.doHandler],
    ["put", new PutStepHandler()],
    ["try", new TryStepHandler(this.doHandler)],
    ["task", new TaskStepHandler()],
    ["in_parallel", this.doHandler],
    ["notify", new NotifyStepHandler()],
    ["agent", new AgentStepHandler()],
  ];

  constructor(
    private jobConfig: JobConfig,
    private resources: Resource[],
    private resourceTypes: ResourceType[],
    private pipelineMaxInFlight?: number,
  ) {
    this.buildID = buildID;
    this.taskRunner = new TaskRunner(this.taskNames, this.resources);
    this.paths = new JobStoragePaths(this.buildID, this.jobConfig.name);
    this.concurrency = new JobConcurrency(
      this.jobConfig.max_in_flight,
      this.pipelineMaxInFlight,
    );
    this.variableResolver = new StepVariableResolver();
    this.ctx = {
      paths: this.paths,
      concurrency: this.concurrency,
      variableResolver: this.variableResolver,
      taskRunner: this.taskRunner,
      resources: this.resources,
      resourceTypes: this.resourceTypes,
      buildID: this.buildID,
      jobName: this.jobConfig.name,
      processStep: (s, pc) => this.processStep(s, pc),
      processStepInternal: (s, pc, a) => this.processStepInternal(s, pc, a),
      runTask: (s, stdin, pc) => this.runTask(s, stdin, pc),
    };
  }

  async run(): Promise<void> {
    const storageKey = this.paths.getBaseStorageKey();
    let failure: unknown = undefined;
    const dependsOn = extractJobDependencies(this.jobConfig.plan);

    const webhookFilter = this.jobConfig.triggers?.webhook?.filter ??
      this.jobConfig.webhook_trigger;

    if (webhookFilter) {
      if (!webhookTrigger(webhookFilter)) {
        storage.set(storageKey, { status: "skipped", dependsOn });
        return;
      }
    }

    const dedupKey = this.jobConfig.triggers?.webhook?.dedup_key;
    if (dedupKey) {
      if (webhookDedup(dedupKey)) {
        storage.set(storageKey, { status: "skipped", dependsOn });
        return;
      }
    }

    const rawParams = this.jobConfig.triggers?.webhook?.params;
    if (rawParams) {
      this.variableResolver.setJobParams(webhookParams(rawParams));
    }

    storage.set(storageKey, { status: "pending", dependsOn });

    let lastCompletedStep = -1;

    try {
      for (let i = 0; i < this.jobConfig.plan.length; i++) {
        await this.processStep(
          this.jobConfig.plan[i],
          zeroPadWithLength(i, this.jobConfig.plan.length),
        );
        lastCompletedStep = i;
      }
      storage.set(storageKey, { status: "success", dependsOn });
    } catch (error) {
      console.error(errorMessage(error));
      failure = error;
      storage.set(storageKey, {
        status: failureStatus(failure),
        dependsOn,
        errorMessage: errorMessage(error),
      });

      // Mark remaining unprocessed steps as skipped
      for (
        let i = lastCompletedStep + 2;
        i < this.jobConfig.plan.length;
        i++
      ) {
        const step = this.jobConfig.plan[i];
        const handler = this.getHandler(step);
        if (handler) {
          const skippedPath = `${this.paths.getBaseStorageKey()}/${
            zeroPadWithLength(i, this.jobConfig.plan.length)
          }/${handler.getIdentifier(step)}`;
          storage.set(skippedPath, { status: "skipped" });
        }
      }
    }

    try {
      const hookName = failureHook(failure);
      if (hookName && this.jobConfig[hookName]) {
        await this.processStep(
          this.jobConfig[hookName]!,
          `hooks/${hookName}`,
        );
      }

      if (this.jobConfig.ensure) {
        await this.processStep(this.jobConfig.ensure, "hooks/ensure");
      }
    } catch (error) {
      console.error(error);
    }

    if (this.jobConfig.assert?.execution) {
      assert.equal(this.taskNames, this.jobConfig.assert.execution);
    }
  }

  private async processStep(step: Step, pathContext: string): Promise<void> {
    const maxAttempts = step.attempts || 1;

    if (maxAttempts <= 1) {
      await this.processStepInternal(step, pathContext);
      return;
    }

    const { ensure, on_success, on_failure, on_error, on_abort, ...innerStep } =
      step as Step & StepHooks;

    let lastError: unknown = null;
    let succeeded = false;

    for (let attempt = 1; attempt <= maxAttempts; attempt++) {
      try {
        await this.processStepInternal(innerStep as Step, pathContext, attempt);
        succeeded = true;
        break;
      } catch (error) {
        lastError = error;
        if (attempt < maxAttempts) {
          console.log(`Attempt ${attempt}/${maxAttempts} failed, retrying...`);
        }
      }
    }

    try {
      const hookName = failureHook(succeeded ? undefined : lastError);
      const hooks = { on_success, on_failure, on_error, on_abort };
      if (hookName && hooks[hookName as keyof typeof hooks]) {
        await this.processStep(
          hooks[hookName as keyof typeof hooks]!,
          `${pathContext}/${hookName}`,
        );
      }
    } finally {
      if (ensure) {
        await this.processStep(ensure, `${pathContext}/ensure`);
      }
    }

    if (!succeeded && lastError) {
      throw lastError;
    }
  }

  private async processStepInternal(
    step: Step,
    pathContext: string,
    attempt?: number,
  ): Promise<void> {
    step = this.variableResolver.injectJobParams(step);

    if (step.across && step.across.length > 0) {
      await this.acrossHandler.process(this.ctx, step, pathContext);
      return;
    }

    const handler = this.getHandler(step);
    if (handler) {
      const resolved = this.paths.withAttemptPath(
        `${pathContext}/${handler.getIdentifier(step)}`,
        attempt,
      );
      await handler.process(this.ctx, step, resolved);
    }
  }

  private getHandler(step: Step): StepHandler | undefined {
    for (const [key, handler] of this.handlers) {
      if (key in step) return handler;
    }
    return undefined;
  }

  private async runTask(
    step: Task,
    stdin?: string,
    pathContext: string = "",
  ): Promise<RunTaskResult> {
    const storageKey = `${this.paths.getBaseStorageKey()}/${pathContext}`;
    let result: RunTaskResult;

    try {
      result = await this.taskRunner.runTask(step, stdin, storageKey);
    } catch (error) {
      if (step.on_error) {
        await this.processStep(step.on_error, `${pathContext}/on_error`);
      }

      throw new TaskErrored(
        `Task ${step.task} errored with message ${error}`,
      );
    }

    if (result.code === 0 && result.status == "complete" && step.on_success) {
      await this.processStep(step.on_success, `${pathContext}/on_success`);
    } else if (
      result.code !== 0 && result.status == "complete" && step.on_failure
    ) {
      await this.processStep(step.on_failure!, `${pathContext}/on_failure`);
    } else if (result.status == "abort" && step.on_abort) {
      await this.processStep(step.on_abort!, `${pathContext}/on_abort`);
    }

    if (step.ensure) {
      await this.processStep(step.ensure!, `${pathContext}/ensure`);
    }

    if (result.code > 0) {
      throw new TaskFailure(
        `Task ${step.task} failed with code ${result.code}`,
      );
    } else if (result.status == "abort") {
      throw new TaskAbort(
        `Task ${step.task} aborted with message ${result.message}`,
      );
    }

    return result;
  }
}
