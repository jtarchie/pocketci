/// <reference path="../../../packages/pocketci/src/global.d.ts" />

import { TaskFailure } from "../task_runner.ts";
import type { StepContext } from "./step_context.ts";
import type { StepHandler } from "./step_handler.ts";
import { processHooks } from "./resource_helpers.ts";

export class NotifyStepHandler implements StepHandler {
  getIdentifier(step: Step): string {
    const s = step as NotifyStep;
    const name = Array.isArray(s.notify) ? s.notify.join("-") : s.notify;
    return `notify/${name}`;
  }

  async process(
    ctx: StepContext,
    step: NotifyStep,
    pathContext: string,
  ): Promise<void> {
    const storageKey = `${ctx.paths.getBaseStorageKey()}/${pathContext}`;
    let failure: unknown = undefined;

    try {
      storage.set(storageKey, { status: "pending" });

      notify.updateJobName(ctx.jobName);
      notify.updateStatus("running");

      const names = Array.isArray(step.notify) ? step.notify : [step.notify];

      if (step.async) {
        for (const name of names) {
          notify.send({ name, message: step.message, async: true });
        }
        storage.set(storageKey, { status: "success" });
      } else {
        if (names.length === 1) {
          await notify.send({
            name: names[0],
            message: step.message,
            async: false,
          });
        } else {
          await notify.sendMultiple({
            names,
            message: step.message,
            async: false,
          });
        }
        storage.set(storageKey, { status: "success" });
      }
    } catch (error) {
      failure = error;
      storage.set(storageKey, { status: "failure" });
    }

    await processHooks(ctx, step, pathContext, storageKey, failure);

    if (failure) {
      throw new TaskFailure(`Notification failed: ${failure}`);
    }
  }
}
