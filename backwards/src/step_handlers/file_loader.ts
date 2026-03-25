/// <reference path="../../../packages/pocketci/src/global.d.ts" />

import type { StepContext } from "./step_context.ts";
import { formatElapsed } from "../utils.ts";

// loadFromURI loads file contents from a URI. Supported schemes:
//   - file:// — reads from a known volume mount (same path format as `file` field)
//   - http:// and https:// — fetches via the global fetch() API
// Throws on unsupported schemes, network errors, or non-OK HTTP responses.
export async function loadFromURI(
  ctx: StepContext,
  uri: string,
  pathContext: string,
): Promise<string> {
  if (uri.startsWith("file://")) {
    const volumePath = uri.slice("file://".length);
    if (volumePath.split("/").includes("..")) {
      throw new Error(
        `file:// URI must not contain ".." path segments: "${uri}"`,
      );
    }
    return loadFileFromVolume(ctx, volumePath, pathContext);
  }

  if (uri.startsWith("http://") || uri.startsWith("https://")) {
    const storageKey =
      `${ctx.paths.getBaseStorageKey()}/${pathContext}/load-uri`;
    const startedAt = new Date().toISOString();
    storage.set(storageKey, {
      status: "pending",
      uri,
      started_at: startedAt,
    });

    try {
      const response = await fetch(uri);
      if (!response.ok) {
        throw new Error(
          `HTTP ${response.status} fetching ${uri}`,
        );
      }
      const content = await response.text();

      storage.set(storageKey, {
        status: "success",
        uri,
        started_at: startedAt,
        elapsed: formatElapsed(startedAt),
        logs: [
          {
            type: "stdout",
            content: `loaded config from ${uri}`,
          },
        ],
      });
      return content;
    } catch (error) {
      const errMsg = error instanceof Error ? error.message : String(error);
      storage.set(storageKey, {
        status: "failure",
        uri,
        started_at: startedAt,
        elapsed: formatElapsed(startedAt),
        errorMessage: errMsg,
        logs: [{ type: "stderr", content: errMsg }],
      });
      throw error;
    }
  }

  throw new Error(
    `unsupported URI scheme in "${uri}"; supported: file://, http://, https://`,
  );
}

// loadConfig loads YAML config from a step's file or URI field.
// Returns the file contents as a string, or null if neither field is set.
export function loadConfig(
  ctx: StepContext,
  step: { file?: string; uri?: string },
  pathContext: string,
): Promise<string | null> {
  if ("file" in step && step.file) {
    return loadFileFromVolume(ctx, step.file, pathContext);
  }

  if ("uri" in step && step.uri) {
    return loadFromURI(ctx, step.uri, pathContext);
  }

  return Promise.resolve(null);
}

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
    const result = await volumes.readFiles(volume.name, [relativePath]);
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
