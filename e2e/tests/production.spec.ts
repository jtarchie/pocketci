import { execSync } from "child_process";
import { mkdirSync, mkdtempSync, writeFileSync } from "fs";
import { tmpdir } from "os";
import { join } from "path";
import { expect, test } from "@playwright/test";

const IS_CI = !!process.env.CI;
const IS_MAC = process.platform === "darwin";
const SCREENSHOTS_DIR = join(
  __dirname,
  "../../docs/public/screenshots/production",
);
const SERVER = "http://localhost:8080";
const E2E_DIR = join(__dirname, "..");
const PIPELINE_NAME = "production-guide";

// Run a pocketci CLI command via go run, returns stdout
function cli(args: string): string {
  return execSync(`go run ../main.go ${args}`, {
    cwd: E2E_DIR,
    encoding: "utf8",
    timeout: 60_000,
  });
}

// Extract the pipeline detail URL from `pipeline set` output
function extractPipelineURL(setOutput: string): string {
  const match = setOutput.match(/URL:\s*(http:\/\/[^\s]+)/);
  if (!match) throw new Error(`Could not find URL in output:\n${setOutput}`);
  return new URL(match[1]).pathname;
}

// Extract run ID from `pipeline trigger` output
function extractRunID(triggerOutput: string): string {
  const match = triggerOutput.match(/\(run:\s*([^\s)]+)\)/);
  if (!match) {
    throw new Error(`Could not find run ID in output:\n${triggerOutput}`);
  }
  return match[1];
}

test.describe("Production Guide", () => {
  test.skip(IS_CI || !IS_MAC, "Only runs locally on macOS");

  let tmpDir: string;
  let pipelinePath: string;
  let pipelineDetailURL: string;

  test.beforeAll(() => {
    mkdirSync(SCREENSHOTS_DIR, { recursive: true });
    tmpDir = mkdtempSync(join(tmpdir(), "pocketci-production-"));
    pipelinePath = join(tmpDir, "ci.yml");

    // Three-job pipeline using native driver for screenshot capture.
    // Uses echo commands so screenshots are fast and don't require Docker.
    writeFileSync(
      pipelinePath,
      `---
jobs:
  - name: lint
    plan:
      - task: golangci-lint
        config:
          platform: linux
          image_resource:
            type: registry-image
            source:
              repository: busybox
          run:
            path: echo
            args: ["lint passed"]

  - name: test
    plan:
      - task: go-test
        config:
          platform: linux
          image_resource:
            type: registry-image
            source:
              repository: busybox
          run:
            path: echo
            args: ["tests passed"]

  - name: build
    plan:
      - task: go-build
        config:
          platform: linux
          image_resource:
            type: registry-image
            source:
              repository: busybox
          run:
            path: echo
            args: ["build complete"]
`,
    );
  });

  test("step 1: server is reachable", async ({ page }) => {
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
    pipelineDetailURL = extractPipelineURL(out);

    await page.goto("/pipelines/");
    await expect(
      page.getByRole("link", { name: PIPELINE_NAME }),
    ).toBeVisible();
    await page.screenshot({
      path: join(SCREENSHOTS_DIR, "02-pipeline-registered.png"),
    });
  });

  test("step 4: run pipeline and show completed jobs", async ({ page }) => {
    const out = cli(
      `pipeline trigger ${PIPELINE_NAME} --server-url ${SERVER}`,
    );
    const runID = extractRunID(out);

    // Wait for all three jobs to complete
    await page.goto(`/runs/${runID}/tasks`);
    await page.waitForSelector("text=Success", { timeout: 60_000 });
    await page.screenshot({
      path: join(SCREENSHOTS_DIR, "03-run-success.png"),
    });
  });

  test("step 3b: pipeline detail page", async ({ page }) => {
    await page.goto(pipelineDetailURL);
    await expect(page.locator("h1")).toBeVisible();
    await page.screenshot({
      path: join(SCREENSHOTS_DIR, "02-pipeline-detail.png"),
    });
  });
});
