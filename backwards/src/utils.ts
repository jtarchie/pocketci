/// <reference path="../../packages/pocketci/src/global.d.ts" />

import { TaskAbort, TaskErrored, TaskFailure } from "./task_runner.ts";

// Extract a clean error message from any thrown value.
// Goja (the JS VM) wraps Go errors in objects that stringify with
// an internal variable prefix (e.g. "h: actual message"). This
// helper peels through to the actual message.
export function errorMessage(error: unknown): string {
  if (error instanceof Error) return error.message;
  return String(error);
}

export function failureStatus(failure: unknown): string {
  if (failure == undefined) return "success";
  if (failure instanceof TaskFailure) return "failure";
  if (failure instanceof TaskAbort) return "abort";
  return "error";
}

export type HookName = "on_success" | "on_failure" | "on_error" | "on_abort";

export function failureHook(failure: unknown): HookName | undefined {
  if (failure == undefined) return "on_success";
  if (failure instanceof TaskFailure) return "on_failure";
  if (failure instanceof TaskErrored) return "on_error";
  if (failure instanceof TaskAbort) return "on_abort";
  return undefined;
}

export function formatElapsed(startedAt: string): string {
  const ms = Date.now() - new Date(startedAt).getTime();
  const totalSeconds = Math.floor(ms / 1000);
  const h = Math.floor(totalSeconds / 3600);
  const m = Math.floor((totalSeconds % 3600) / 60);
  const s = totalSeconds % 60;
  if (h > 0) return `${h}h ${m}m ${s}s`;
  if (m > 0) return `${m}m ${s}s`;
  return `${s}s`;
}

export function safeStorageGet(key: string): unknown {
  try {
    return storage.get(key);
  } catch {
    return null;
  }
}

export function getBuildID(): string {
  return (typeof pipelineContext !== "undefined" && pipelineContext.runID)
    ? pipelineContext.runID
    : String(Date.now());
}

export function extractJobDependencies(plan: Step[]): string[] {
  const dependencies: string[] = [];
  for (const step of plan) {
    if ("get" in step && step.passed) {
      for (const passedJob of step.passed) {
        if (!dependencies.includes(passedJob)) {
          dependencies.push(passedJob);
        }
      }
    }
  }
  return dependencies;
}
