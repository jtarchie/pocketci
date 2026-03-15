/// <reference path="../../../packages/pocketci/src/global.d.ts" />

import type { StepContext } from "./step_context.ts";

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
  storage.set(storageKey, {
    status: "pending",
    file,
    volume: mountName,
  });

  try {
    const result = await runtime.readFilesFromVolume(volume.name, relativePath);
    const content = result[relativePath];
    if (content === undefined) {
      storage.set(storageKey, {
        status: "failure",
        file,
        volume: mountName,
        errorMessage:
          `file "${relativePath}" not found in volume "${mountName}"`,
      });
      throw new Error(
        `file "${relativePath}" not found in volume "${mountName}"`,
      );
    }
    storage.set(storageKey, {
      status: "success",
      file,
      volume: mountName,
    });
    return content;
  } catch (error) {
    storage.set(storageKey, {
      status: "failure",
      file,
      volume: mountName,
      errorMessage: error instanceof Error ? error.message : String(error),
    });
    throw error;
  }
}
