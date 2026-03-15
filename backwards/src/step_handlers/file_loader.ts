/// <reference path="../../../packages/pocketci/src/global.d.ts" />

import type { StepContext } from "./step_context.ts";

// loadFileFromVolume reads a file from a volume mount by spawning a temporary
// busybox task that cats the file. The file path must start with the mount
// name (e.g. "repo/path/to/file.yml"). Returns the file contents as a string.
// Throws on failure with a descriptive error including the volume name.
export async function loadFileFromVolume(
  ctx: StepContext,
  file: string,
  pathContext: string,
): Promise<string> {
  const mountName = file.split("/")[0];
  const result = await ctx.runTask(
    {
      task: `get-file-${file}`,
      config: {
        image_resource: {
          type: "registry-image",
          source: { repository: "busybox" },
        },
        inputs: [{ name: mountName }],
        run: { path: "sh", args: ["-c", `cat ${file}`] },
      },
      assert: { code: 0 },
    },
    undefined,
    pathContext,
  );
  return result.stdout;
}
