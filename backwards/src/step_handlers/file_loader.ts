/// <reference path="../../../packages/pocketci/src/global.d.ts" />

import type { StepContext } from "./step_context.ts";
import { formatElapsed } from "../utils.ts";

// loadFileFromVolume reads a file from a volume mount using the runtime's
// direct volume access API. The file path must start with the mount name
// (e.g. "repo/path/to/file.yml"). Returns the file contents as a string.
// Throws on failure with a descriptive error including the volume name.
export async function loadFileFromVolume(
  ctx: StepContext,
  file: string,
  pathContext: string,
): Promise<string> {
  const mountName = file.split("/")[0];
  const relativePath = file.substring(mountName.length + 1);

  // Look up the actual volume name from known mounts
  const knownMounts = ctx.taskRunner.getKnownMounts();
  const volume = knownMounts[mountName];
  if (!volume) {
    throw new Error(
      `volume "${mountName}" not found in known mounts`,
    );
  }

  const storageKey =
    `${ctx.paths.getBaseStorageKey()}/${pathContext}/load-file`;
  const startedAt = new Date().toISOString();
  storage.set(storageKey, {
    status: "pending",
    file,
    volume: mountName,
    started_at: startedAt,
  });

  try {
    const result = await runtime.readFilesFromVolume(volume.name, relativePath);
    const content = result[relativePath];
    if (content === undefined) {
      const errMsg =
        `file "${relativePath}" not found in volume "${mountName}"`;
      storage.set(storageKey, {
        status: "failure",
        file,
        volume: mountName,
        started_at: startedAt,
        elapsed: formatElapsed(startedAt),
        errorMessage: errMsg,
        logs: [{ type: "stderr", content: errMsg }],
      });
      throw new Error(errMsg);
    }
    storage.set(storageKey, {
      status: "success",
      file,
      volume: mountName,
      started_at: startedAt,
      elapsed: formatElapsed(startedAt),
      logs: [
        {
          type: "stdout",
          content: `loaded ${file} from volume ${mountName}`,
        },
      ],
    });
    return content;
  } catch (error) {
    const errMsg = error instanceof Error ? error.message : String(error);
    storage.set(storageKey, {
      status: "failure",
      file,
      volume: mountName,
      started_at: startedAt,
      elapsed: formatElapsed(startedAt),
      errorMessage: errMsg,
      logs: [{ type: "stderr", content: errMsg }],
    });
    throw error;
  }
}
