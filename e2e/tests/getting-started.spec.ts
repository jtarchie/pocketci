import { execSync } from "child_process";
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "fs";
import { tmpdir } from "os";
import { join } from "path";
import { expect, test } from "@playwright/test";

const IS_CI = !!process.env.CI;
const IS_MAC = process.platform === "darwin";
const SCREENSHOTS_DIR = join(
  __dirname,
  "../../docs/public/screenshots/getting-started",
);
const SERVER = "http://localhost:8080";
const E2E_DIR = join(__dirname, "..");
const PIPELINE_NAME = "getting-started-guide";

// Run a pocketci CLI command via go run, returns stdout
function cli(args: string): string {
  return execSync(`go run ../main.go ${args}`, {
    cwd: E2E_DIR,
    encoding: "utf8",
    timeout: 60_000,
  });
}

// Extract the pipeline detail URL from `pipeline set` output
// e.g. "  URL: http://localhost:8080/pipelines/abc123/" → "/pipelines/abc123/"
function extractPipelineURL(setOutput: string): string {
  const match = setOutput.match(/URL:\s*(http:\/\/[^\s]+)/);
  if (!match) throw new Error(`Could not find URL in output:\n${setOutput}`);
  return new URL(match[1]).pathname;
}

// Extract run ID from `pipeline trigger` output
// e.g. "Pipeline 'foo' triggered successfully (run: abc123)" → "abc123"
function extractRunID(triggerOutput: string): string {
  const match = triggerOutput.match(/\(run:\s*([^\s)]+)\)/);
  if (!match) {
    throw new Error(`Could not find run ID in output:\n${triggerOutput}`);
  }
  return match[1];
}

test.describe("Getting Started Guide", () => {
  test.skip(IS_CI || !IS_MAC, "Only runs locally on macOS");

  let tmpDir: string;
  let pipelinePath: string;

  test.beforeAll(() => {
    mkdirSync(SCREENSHOTS_DIR, { recursive: true });
    tmpDir = mkdtempSync(join(tmpdir(), "pocketci-getting-started-"));
    pipelinePath = join(tmpDir, "hello.yml");

    writeFileSync(
      pipelinePath,
      `---
jobs:
  - name: hello
    plan:
      - task: say-hello
        config:
          platform: linux
          image_resource:
            type: registry-image
            source:
              repository: busybox
          run:
            path: echo
            args: ["Hello from PocketCI!"]
`,
    );
  });

  test("step 1: server is reachable and pipelines page loads", async ({ page }) => {
    await page.goto("/pipelines/");
    await expect(
      page.locator("h1").filter({ hasText: "Pipelines" }),
    ).toBeVisible();
    await page.screenshot({
      path: join(SCREENSHOTS_DIR, "01-server-running.png"),
    });
  });

  test("step 3: register pipeline via CLI", async ({ page }) => {
    const out = cli(
      `pipeline set ${pipelinePath} ` +
        `--server-url ${SERVER} --name ${PIPELINE_NAME} --driver native`,
    );
    // Persist the pipeline detail URL for subsequent tests
    writeFileSync(join(tmpDir, "pipeline-url.txt"), extractPipelineURL(out));

    await page.goto("/pipelines/");
    await expect(
      page.getByRole("link", { name: PIPELINE_NAME }),
    ).toBeVisible();
    await page.screenshot({
      path: join(SCREENSHOTS_DIR, "02-pipeline-registered.png"),
    });
  });

  test("step 3b: list pipelines via CLI", () => {
    const out = cli(`pipeline ls --server-url ${SERVER}`);
    expect(out).toContain(PIPELINE_NAME);
  });

  test("step 4: run pipeline via CLI and verify output", async ({ page }) => {
    const out = cli(
      `pipeline run ${PIPELINE_NAME} --server-url ${SERVER} --no-workdir`,
    );
    expect(out).toContain("Hello from PocketCI!");

    const pipelineDetailURL = readFileSync(
      join(tmpDir, "pipeline-url.txt"),
      "utf8",
    );
    await page.goto(pipelineDetailURL);
    await page.waitForSelector("text=Success", { timeout: 30_000 });
    await page.screenshot({
      path: join(SCREENSHOTS_DIR, "03-run-success.png"),
    });
  });

  test("step 5: trigger pipeline async and show completed run", async ({ page }) => {
    const out = cli(
      `pipeline trigger ${PIPELINE_NAME} --server-url ${SERVER}`,
    );
    const runID = extractRunID(out);

    // Navigate to the run detail page and wait for it to complete
    await page.goto(`/runs/${runID}/tasks`);
    await page.waitForSelector("text=Success", { timeout: 30_000 });
    await page.screenshot({
      path: join(SCREENSHOTS_DIR, "04-triggered-run-completed.png"),
    });
  });
});
