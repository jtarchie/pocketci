import pMap from "p-map";
import { TaskAbort, TaskErrored, TaskFailure } from "./task_runner.ts";

export interface ConcurrencyResult {
  failed: boolean;
  firstError?: unknown;
}

export class JobConcurrency {
  constructor(
    private jobMaxInFlight?: number,
    private pipelineMaxInFlight?: number,
  ) {}

  private getDefaultMaxInFlight(): number | undefined {
    if (this.jobMaxInFlight && this.jobMaxInFlight > 0) {
      return this.jobMaxInFlight;
    }

    if (this.pipelineMaxInFlight && this.pipelineMaxInFlight > 0) {
      return this.pipelineMaxInFlight;
    }

    return undefined;
  }

  private resolveMaxInFlight(localLimit?: number): number {
    const fallback = this.getDefaultMaxInFlight();
    if (fallback && fallback > 0) {
      return fallback;
    }

    if (localLimit && localLimit > 0) {
      return localLimit;
    }

    return Number.MAX_SAFE_INTEGER;
  }

  async runWithConcurrencyLimit<T>(
    items: T[],
    worker: (item: T, index: number) => Promise<void>,
    localLimit?: number,
    failFast: boolean = false,
  ): Promise<ConcurrencyResult> {
    if (items.length === 0) {
      return { failed: false };
    }

    const concurrency = Math.max(
      1,
      Math.min(this.resolveMaxInFlight(localLimit), items.length),
    );

    const allErrors: unknown[] = [];

    try {
      await pMap(
        items,
        async (item, index) => {
          try {
            await worker(item, index);
          } catch (error) {
            allErrors.push(error);
            throw error;
          }
        },
        { concurrency, stopOnError: failFast },
      );
    } catch {
      // errors already collected in allErrors
    }

    if (allErrors.length === 0) {
      return { failed: false };
    }

    const firstError = allErrors.find((error) => error instanceof TaskAbort) ??
      allErrors.find((error) => error instanceof TaskErrored) ??
      allErrors.find((error) => error instanceof TaskFailure) ??
      allErrors[0];

    return { failed: true, firstError };
  }
}
