/// <reference path="../../packages/pocketci/src/global.d.ts" />

import { formatElapsed } from "./utils.ts";
import { safeStorageGet } from "./utils.ts";

const defaultAssertOutputTimeoutMs = 3000;

export class TaskRunner {
  private knownMounts: KnownMounts = {};

  constructor(
    private taskNames: string[],
    private resources: Resource[],
  ) {}

  async runTask(
    step: Task,
    stdin: string | undefined,
    storageKey: string,
  ): Promise<RunTaskResult> {
    const taskStorageKey = storageKey;
    const startedAt = new Date().toISOString();
    const mounts = await this.prepareMounts(step);
    this.taskNames.push(step.task);

    storage.set(
      taskStorageKey,
      {
        status: "pending",
        started_at: startedAt,
      },
    );

    let result: RunTaskResult;

    // Determine which image to use
    let image: string;
    if (step.image) {
      // Look up the resource and use its repository
      const resource = this.resources.find((r) => r.name === step.image);
      if (!resource) {
        throw new Error(`Image resource '${step.image}' not found`);
      }
      if (resource.type !== "registry-image") {
        throw new Error(
          `Image resource '${step.image}' must be of type 'registry-image', got '${resource.type}'`,
        );
      }
      image = resource.source.repository;
    } else if (step.config?.image) {
      // TaskConfig shorthand: direct image string
      image = step.config.image;
    } else {
      // Fall back to image_resource in config
      image = step.config?.image_resource?.source?.repository!;
    }

    const logs: Array<{ type: "stdout" | "stderr"; content: string }> = [];

    try {
      result = await runtime.run({
        command: {
          path: step.config.run!.path,
          args: step.config.run!.args || [],
          user: step.config.run!.user,
        },
        container_limits: step.config.container_limits,
        env: step.config.env,
        image: image,
        name: step.task,
        mounts: mounts,
        privileged: step.privileged ?? false,
        stdin: stdin ?? "",
        timeout: step.timeout,
        storage_key: taskStorageKey,
        onOutput: (stream: "stdout" | "stderr", data: string) => {
          logs.push({ type: stream, content: data });

          storage.set(taskStorageKey, {
            status: "running",
            started_at: startedAt,
            logs: logs.slice(),
          });
        },
      });

      let status = "success";
      if (result.status == "abort") {
        status = "abort";
      } else if (result.code !== 0) {
        status = "failure";
      }

      storage.set(
        taskStorageKey,
        {
          status: status,
          code: result.code,
          started_at: startedAt,
          elapsed: formatElapsed(startedAt),
          logs: logs.slice(),
        },
      );

      this.validateTaskResult(step, result, taskStorageKey);

      return result;
    } catch (error) {
      storage.set(taskStorageKey, {
        status: "error",
        started_at: startedAt,
        elapsed: formatElapsed(startedAt),
      });

      throw new TaskErrored(
        `Task ${step.task} errored with message ${error}`,
      );
    }
  }

  getKnownMounts(): KnownMounts {
    return this.knownMounts;
  }

  private async prepareMounts(step: Task): Promise<KnownMounts> {
    const mounts: KnownMounts = {};

    const inputs = step.config.inputs || [];
    const outputs = step.config.outputs || [];
    const caches = step.config.caches || [];

    for (const mount of inputs) {
      this.knownMounts[mount.name] ||= await runtime.createVolume();
      mounts[mount.name] = this.knownMounts[mount.name];
    }

    for (const mount of outputs) {
      this.knownMounts[mount.name] ||= await runtime.createVolume();
      mounts[mount.name] = this.knownMounts[mount.name];
    }

    // Caches use a stable name based on path so they persist across pipeline runs.
    // The path is normalized to create a safe volume name (e.g., "/cache/go-build" -> "cache-go-build")
    for (const cache of caches) {
      const cacheName = this.pathToCacheName(cache.path);
      // Use a global cache registry to share caches across tasks
      this.knownMounts[cacheName] ||= await runtime.createVolume({
        name: cacheName,
      });
      // Mount at the cache path - strip leading slash as mounts are relative to workdir
      const mountPath = cache.path.replace(/^\/+/, "");
      mounts[mountPath] = this.knownMounts[cacheName];
    }

    return mounts;
  }

  // Convert a cache path to a safe volume name
  private pathToCacheName(path: string): string {
    // Remove leading slashes and replace special chars with dashes
    return "cache-" + path
      .replace(/^\/+/, "")
      .replace(/[^a-zA-Z0-9]+/g, "-")
      .replace(/-+/g, "-")
      .replace(/-$/, "")
      .toLowerCase();
  }

  private validateTaskResult(
    step: Task,
    result: RunTaskResult,
    taskStorageKey: string,
  ): void {
    if (step.assert?.stdout && step.assert.stdout.trim() !== "") {
      this.assertOutputEventuallyContains(
        "stdout",
        step.assert.stdout,
        result,
        taskStorageKey,
      );
    }

    if (step.assert?.stderr && step.assert.stderr.trim() !== "") {
      this.assertOutputEventuallyContains(
        "stderr",
        step.assert.stderr,
        result,
        taskStorageKey,
      );
    }

    if (typeof step.assert?.code === "number") {
      assert.equal(result.code, step.assert.code);
    }
  }

  private assertOutputEventuallyContains(
    stream: "stdout" | "stderr",
    expected: string,
    result: RunTaskResult,
    taskStorageKey: string,
  ) {
    assert.eventuallyContainsString(
      () => this.getLatestTaskOutput(stream, result, taskStorageKey),
      expected,
      defaultAssertOutputTimeoutMs,
      50,
    );
  }

  private getLatestTaskOutput(
    stream: "stdout" | "stderr",
    result: RunTaskResult,
    taskStorageKey: string,
  ): string {
    let output = stream === "stdout" ? result.stdout : result.stderr;

    const taskStatus = safeStorageGet(taskStorageKey) as {
      logs?: Array<{ type?: string; content?: string }>;
    } | null;

    if (taskStatus?.logs && Array.isArray(taskStatus.logs)) {
      const buffered = taskStatus.logs
        .filter((entry) =>
          entry?.type === stream && typeof entry?.content === "string"
        )
        .map((entry) => entry.content as string)
        .join("");

      if (buffered.length > output.length) {
        output = buffered;
      }
    }

    return output;
  }
}

class CustomError extends Error {
  constructor(message: string) {
    super(message);
    this.name = this.constructor.name;
  }
}

export class TaskFailure extends CustomError {}
export class TaskErrored extends CustomError {}
export class TaskAbort extends CustomError {}
