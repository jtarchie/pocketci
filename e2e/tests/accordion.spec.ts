import { type APIRequestContext, expect, test } from "@playwright/test";

// Helper to generate unique pipeline names
function uniqueName(base: string): string {
  return `${base}-${Date.now()}-${Math.random().toString(36).substring(7)}`;
}

// Helper to create a pipeline and return its ID
async function createPipeline(
  request: APIRequestContext,
  name: string,
  content: string,
  driver: string = "native",
): Promise<string> {
  const response = await request.put(
    `/api/pipelines/${encodeURIComponent(name)}`,
    {
      data: { content, driver: driver },
    },
  );
  expect(response.ok()).toBeTruthy();
  const data = await response.json();
  return data.id;
}

test.describe("Task Accordion State during Live Updates", () => {
  // Docker-based tests can be slow
  test.setTimeout(120000);

  test(
    "expanded task stays open across htmx poll cycles",
    async ({ page, request }) => {
      // Pipeline that runs long enough for us to interact while it is active.
      // The "slow-task" sleeps for 15 s so we can catch multiple 3-second
      // htmx poll cycles while it is still running.
      const pipelineName = uniqueName("accordion-poll-test");
      await createPipeline(
        request,
        pipelineName,
        `export const pipeline = async () => {
          await runtime.run({
            name: "slow-task",
            image: "alpine",
            command: { path: "sleep", args: ["15"] },
          });
        };`,
        "docker",
      );

      // ── Navigate to the pipeline detail page ──────────────────────────────
      await page.goto("/pipelines/");
      await page.getByRole("link", { name: pipelineName }).click();

      // Trigger the pipeline
      await page.getByRole("button", { name: /trigger/i }).click();
      await expect(page.getByText(/triggered successfully/i)).toBeVisible({
        timeout: 5000,
      });

      // ── Open the tasks page for this run ──────────────────────────────────
      const tasksLink = page.getByRole("link", { name: "Tasks" }).first();
      await expect(tasksLink).toBeVisible({ timeout: 15000 });
      await tasksLink.click();

      await expect(page).toHaveURL(/\/runs\/[^/]+\/tasks/);

      // ── Wait for at least one task item to appear ─────────────────────────
      // The run must be "active" for htmx polling to kick in; the Live
      // indicator confirms that.
      const liveIndicator = page.getByText("Live", { exact: true });
      await expect(liveIndicator).toBeVisible({ timeout: 30000 });

      const taskItem = page.locator(".task-item").first();
      await expect(taskItem).toBeVisible({ timeout: 30000 });

      // ── Expand the accordion ────────────────────────────────────────────
      // Click the <summary> to open the <details>
      await taskItem.locator("summary").click();
      await expect(taskItem).toHaveJSProperty("open", true);

      // ── Wait for two htmx poll cycles (3 s each) ─────────────────────────
      await page.waitForTimeout(7000);

      // The task should still be expanded after the polls.
      // Before the fix idiomorph had no stable id and replaced the element,
      // losing the `open` attribute.
      await expect(taskItem).toHaveJSProperty("open", true, {
        message:
          "Task accordion should remain open after htmx polling updated the DOM",
      });
    },
  );
});
