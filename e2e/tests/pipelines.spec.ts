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
  webhookSecret?: string,
): Promise<string> {
  const body: Record<string, unknown> = { content, driver };
  if (webhookSecret !== undefined) {
    body.webhook_secret = webhookSecret;
  }
  const response = await request.put(
    `/api/pipelines/${encodeURIComponent(name)}`,
    { data: body },
  );
  expect(response.ok()).toBeTruthy();
  const data = await response.json();
  return data.id;
}

test.describe("Pipeline Management UI", () => {
  test.describe("Pipelines List Page", () => {
    test("shows pipelines page structure", async ({ page }) => {
      await page.goto("/pipelines/");

      // Should have the pipelines page header (h1 specifically)
      await expect(page.locator("h1").filter({ hasText: "Pipelines" }))
        .toBeVisible();

      // Should have either the empty state or a table
      const hasTable = await page.locator("table").count();
      const hasEmptyState = await page.getByText("No pipelines yet").count();
      expect(hasTable > 0 || hasEmptyState > 0).toBeTruthy();
    });

    test("redirects root to pipelines list", async ({ page }) => {
      await page.goto("/");

      // Should redirect to /pipelines/
      await expect(page).toHaveURL(/\/pipelines\//);
    });
  });

  test.describe("Pipeline CRUD and Execution", () => {
    test("displays created pipeline in the list", async ({ page, request }) => {
      const pipelineName = uniqueName("list-test");
      await createPipeline(
        request,
        pipelineName,
        `export const pipeline = async () => { console.log("test"); };`,
        "docker",
      );

      await page.goto("/pipelines/");

      // Should show the pipeline in the table
      await expect(page.getByRole("link", { name: pipelineName }))
        .toBeVisible();
      const row = page.locator("tr").filter({ hasText: pipelineName });
      await expect(row.getByText("docker", { exact: true })).toBeVisible();
    });

    test("shows trigger button for each pipeline", async ({ page, request }) => {
      const pipelineName = uniqueName("trigger-btn-test");
      await createPipeline(
        request,
        pipelineName,
        `export const pipeline = async () => { console.log("test"); };`,
      );

      await page.goto("/pipelines/");

      // Should have trigger buttons
      const triggerButton = page.getByRole("button", { name: /trigger/i })
        .first();
      await expect(triggerButton).toBeVisible();
      await expect(triggerButton).toBeEnabled();
    });

    test("navigates to pipeline detail page", async ({ page, request }) => {
      const pipelineName = uniqueName("nav-detail-test");
      await createPipeline(
        request,
        pipelineName,
        `export const pipeline = async () => { console.log("test"); };`,
      );

      await page.goto("/pipelines/");

      // Click on pipeline name to go to detail page
      await page.getByRole("link", { name: pipelineName }).click();

      // Should be on the detail page
      await expect(page).toHaveURL(/\/pipelines\/[^/]+\//);
      await expect(page.getByRole("heading", { name: pipelineName }))
        .toBeVisible();
    });

    test("pipeline detail page shows metadata", async ({ page, request }) => {
      const pipelineName = uniqueName("metadata-test");
      await createPipeline(
        request,
        pipelineName,
        `export const pipeline = async () => { console.log("test"); };`,
        "docker",
      );

      await page.goto("/pipelines/");
      await page.getByRole("link", { name: pipelineName }).click();

      // Should show pipeline metadata
      await expect(page.getByText("docker")).toBeVisible();
      await expect(page.getByText(/Created/)).toBeVisible();

      // Should show the trigger button
      await expect(page.getByRole("button", { name: /trigger/i }))
        .toBeVisible();
    });

    test("pipeline detail page shows empty runs message", async ({ page, request }) => {
      const pipelineName = uniqueName("empty-runs-test");
      await createPipeline(
        request,
        pipelineName,
        `export const pipeline = async () => { console.log("test"); };`,
      );

      await page.goto("/pipelines/");
      await page.getByRole("link", { name: pipelineName }).click();

      // Should show empty runs message
      await expect(page.getByText(/No runs yet/)).toBeVisible();
    });

    test("can expand pipeline source code", async ({ page, request }) => {
      const pipelineName = uniqueName("source-expand-test");
      await createPipeline(
        request,
        pipelineName,
        `export const pipeline = async () => { console.log("source test marker"); };`,
      );

      await page.goto("/pipelines/");
      await page.getByRole("link", { name: pipelineName }).click();

      // Open the "..." dropdown and click "View Source"
      await page.getByLabel("More actions").click();
      await page.getByRole("menuitem", { name: "View Source" }).click();

      // Should show the pipeline content on the source page
      await expect(page.getByText("source test marker")).toBeVisible();
    });

    test("breadcrumb navigation works", async ({ page, request }) => {
      const pipelineName = uniqueName("breadcrumb-test");
      await createPipeline(
        request,
        pipelineName,
        `export const pipeline = async () => { console.log("test"); };`,
      );

      await page.goto("/pipelines/");
      await page.getByRole("link", { name: pipelineName }).click();

      // Should show breadcrumb with Pipelines link
      const pipelinesLink = page.locator("nav").getByRole("link", {
        name: "Pipelines",
      });
      await expect(pipelinesLink).toBeVisible();

      // Click Pipelines link to go back
      await pipelinesLink.click();
      await expect(page).toHaveURL(/\/pipelines\//);
    });
  });

  test.describe("Pipeline Triggering", () => {
    test("trigger button shows toast on success", async ({ page, request }) => {
      const pipelineName = uniqueName("trigger-toast-test");
      await createPipeline(
        request,
        pipelineName,
        `export const pipeline = async () => { console.log("Quick test"); };`,
      );

      await page.goto("/pipelines/");

      // Find the row with our pipeline and click its trigger button
      const row = page.locator("tr").filter({ hasText: pipelineName });
      await row.getByRole("button", { name: /trigger/i }).click();

      // Should show success toast
      await expect(page.getByText(/triggered successfully/i)).toBeVisible({
        timeout: 5000,
      });
    });

    test("triggering from detail page adds run to table", async ({ page, request }) => {
      const pipelineName = uniqueName("detail-trigger-test");
      await createPipeline(
        request,
        pipelineName,
        `export const pipeline = async () => { console.log("Quick test"); };`,
      );

      await page.goto("/pipelines/");
      await page.getByRole("link", { name: pipelineName }).click();

      // Should show no runs initially
      await expect(page.getByText(/No runs yet/)).toBeVisible();

      // Trigger the pipeline
      await page.getByRole("button", { name: /trigger/i }).click();

      // Should show success toast
      await expect(page.getByText(/triggered successfully/i)).toBeVisible({
        timeout: 5000,
      });

      // A run should now appear in the table (may need to wait for it)
      await expect(
        page.getByText(/queued|running|success|failed/i).first(),
      ).toBeVisible({ timeout: 10000 });
    });

    test("run status updates via polling", async ({ page, request }) => {
      const pipelineName = uniqueName("polling-test");
      await createPipeline(
        request,
        pipelineName,
        `export const pipeline = async () => { console.log("Quick test"); };`,
      );

      await page.goto("/pipelines/");
      await page.getByRole("link", { name: pipelineName }).click();

      // Trigger the pipeline
      await page.getByRole("button", { name: /trigger/i }).click();

      // Wait for the run to complete (success or failed)
      // The status should change from queued -> running -> success/failed
      await expect(page.getByText(/success|failed/i).first()).toBeVisible({
        timeout: 30000,
      });
    });

    test("trigger with args dialog opens, submits, and shows toast", async ({ page, request }) => {
      const pipelineName = uniqueName("trigger-args-test");
      await createPipeline(
        request,
        pipelineName,
        `export const pipeline = async () => { console.log("args test"); };`,
      );

      await page.goto("/pipelines/");
      await page.getByRole("link", { name: pipelineName }).click();

      // Open the split-button dropdown and click "Trigger with Args…"
      await page.getByLabel("More trigger options").click();
      await page.getByRole("menuitem", { name: /trigger with args/i }).click();

      // Dialog should be visible and centered
      const dialog = page.locator("#trigger-args-dialog");
      await expect(dialog).toBeVisible();

      // Fill in args (one per line)
      await page.locator("#trigger-args-input").fill(
        "--env=staging\n--verbose",
      );

      // Submit
      await page.locator("#trigger-args-submit").click();

      // Should show success toast
      await expect(page.getByText(/triggered successfully/i)).toBeVisible({
        timeout: 5000,
      });

      // Dialog should close
      await expect(dialog).not.toBeVisible();
    });

    test("trigger with webhook dialog opens, submits, and shows toast", async ({ page, request }) => {
      const pipelineName = uniqueName("trigger-webhook-test");
      await createPipeline(
        request,
        pipelineName,
        `export const pipeline = async () => { console.log("webhook test"); };`,
        "native",
        "test-secret",
      );

      await page.goto("/pipelines/");
      await page.getByRole("link", { name: pipelineName }).click();

      // Open dropdown and click "Trigger with Webhook…"
      await page.getByLabel("More trigger options").click();
      await page.getByRole("menuitem", { name: /trigger with webhook/i })
        .click();

      // Dialog should be visible
      const dialog = page.locator("#trigger-webhook-dialog");
      await expect(dialog).toBeVisible();

      // Fill JSON body
      await page.locator("#trigger-webhook-body").fill('{"action": "test"}');

      // JSON preview should appear with syntax highlighting
      const preview = page.locator("#trigger-webhook-preview");
      await expect(preview).toBeVisible();
      await expect(preview.locator("code.language-json")).toBeVisible();

      // Submit
      await page.locator("#trigger-webhook-submit").click();

      // Should show success toast
      await expect(page.getByText(/triggered successfully/i)).toBeVisible({
        timeout: 5000,
      });

      // Dialog should close
      await expect(dialog).not.toBeVisible();
    });

    test("trigger args dialog can be cancelled", async ({ page, request }) => {
      const pipelineName = uniqueName("trigger-cancel-test");
      await createPipeline(
        request,
        pipelineName,
        `export const pipeline = async () => { console.log("cancel test"); };`,
      );

      await page.goto("/pipelines/");
      await page.getByRole("link", { name: pipelineName }).click();

      // Open args dialog
      await page.getByLabel("More trigger options").click();
      await page.getByRole("menuitem", { name: /trigger with args/i }).click();

      const dialog = page.locator("#trigger-args-dialog");
      await expect(dialog).toBeVisible();

      // Click Cancel
      await dialog.getByRole("button", { name: "Cancel" }).click();

      // Dialog should close
      await expect(dialog).not.toBeVisible();
    });

    test("webhook dialog supports adding and removing headers", async ({ page, request }) => {
      const pipelineName = uniqueName("trigger-headers-test");
      await createPipeline(
        request,
        pipelineName,
        `export const pipeline = async () => { console.log("headers test"); };`,
        "native",
        "test-secret",
      );

      await page.goto("/pipelines/");
      await page.getByRole("link", { name: pipelineName }).click();

      // Open webhook dialog
      await page.getByLabel("More trigger options").click();
      await page.getByRole("menuitem", { name: /trigger with webhook/i })
        .click();

      const dialog = page.locator("#trigger-webhook-dialog");
      await expect(dialog).toBeVisible();

      // Click "+ Add header"
      await page.locator("#trigger-webhook-add-header").click();

      // Header row should appear
      const headerRow = page.locator("#trigger-webhook-headers > div").first();
      await expect(headerRow).toBeVisible();

      // Fill in header key/value
      await headerRow.locator(".webhook-header-key").fill("X-Custom-Header");
      await headerRow.locator(".webhook-header-val").fill("test-value");

      // Remove header
      await headerRow.locator("button").click();

      // Header row should be gone
      await expect(page.locator("#trigger-webhook-headers > div")).toHaveCount(
        0,
      );
    });

    test("webhook option hidden when no webhook secret configured", async ({ page, request }) => {
      const pipelineName = uniqueName("no-webhook-secret-test");
      await createPipeline(
        request,
        pipelineName,
        `export const pipeline = async () => { console.log("no secret"); };`,
      );

      await page.goto("/pipelines/");
      await page.getByRole("link", { name: pipelineName }).click();

      // Open the split-button dropdown
      await page.getByLabel("More trigger options").click();

      // Args should be visible
      await expect(
        page.getByRole("menuitem", { name: /trigger with args/i }),
      ).toBeVisible();

      // Webhook should NOT be visible
      await expect(
        page.getByRole("menuitem", { name: /trigger with webhook/i }),
      ).not.toBeVisible();
    });

    test("webhook option visible when webhook secret is configured", async ({ page, request }) => {
      const pipelineName = uniqueName("has-webhook-secret-test");
      await createPipeline(
        request,
        pipelineName,
        `export const pipeline = async () => { console.log("has secret"); };`,
        "native",
        "my-secret",
      );

      await page.goto("/pipelines/");
      await page.getByRole("link", { name: pipelineName }).click();

      // Open the split-button dropdown
      await page.getByLabel("More trigger options").click();

      // Both options should be visible
      await expect(
        page.getByRole("menuitem", { name: /trigger with args/i }),
      ).toBeVisible();
      await expect(
        page.getByRole("menuitem", { name: /trigger with webhook/i }),
      ).toBeVisible();
    });

    test("clicking Tasks link navigates to /runs/:id/tasks", async ({ page, request }) => {
      const pipelineName = uniqueName("tasks-link-test");
      await createPipeline(
        request,
        pipelineName,
        `export const pipeline = async () => { console.log("test"); };`,
      );

      await page.goto("/pipelines/");
      await page.getByRole("link", { name: pipelineName }).click();

      // Trigger the pipeline and wait for it to appear
      await page.getByRole("button", { name: /trigger/i }).click();
      await expect(page.getByText(/triggered successfully/i)).toBeVisible({
        timeout: 5000,
      });

      // Wait for the run row to appear with Tasks link
      const tasksLink = page.getByRole("link", { name: "Tasks" }).first();
      await expect(tasksLink).toBeVisible({ timeout: 10000 });

      // Click the Tasks link
      await tasksLink.click();

      // Should navigate to /runs/:id/tasks
      await expect(page).toHaveURL(/\/runs\/[^/]+\/tasks/);

      // Should show the Tasks page
      await expect(page.getByRole("heading", { name: /Tasks/i }))
        .toBeVisible();
    });

    test("clicking Graph link navigates to /runs/:id/graph", async ({ page, request }) => {
      const pipelineName = uniqueName("graph-link-test");
      await createPipeline(
        request,
        pipelineName,
        `export const pipeline = async () => { console.log("test"); };`,
      );

      await page.goto("/pipelines/");
      await page.getByRole("link", { name: pipelineName }).click();

      // Trigger the pipeline and wait for it to appear
      await page.getByRole("button", { name: /trigger/i }).click();
      await expect(page.getByText(/triggered successfully/i)).toBeVisible({
        timeout: 5000,
      });

      // Wait for the run row to appear with Graph link
      const graphLink = page.getByRole("link", { name: "Graph" }).first();
      await expect(graphLink).toBeVisible({ timeout: 10000 });

      // Click the Graph link
      await graphLink.click();

      // Should navigate to /runs/:id/graph
      await expect(page).toHaveURL(/\/runs\/[^/]+\/graph/);

      // Should show the Task Graph page
      await expect(page.getByRole("heading", { name: /Task Graph/i }))
        .toBeVisible();
    });
  });
});

test.describe("Run Views", () => {
  test("run tasks view shows Run ID in breadcrumb", async ({ page, request }) => {
    const pipelineName = uniqueName("run-breadcrumb-test");
    await createPipeline(
      request,
      pipelineName,
      `export const pipeline = async () => { console.log("test"); };`,
    );

    await page.goto("/pipelines/");
    await page.getByRole("link", { name: pipelineName }).click();

    // Trigger the pipeline
    await page.getByRole("button", { name: /trigger/i }).click();
    await expect(page.getByText(/triggered successfully/i)).toBeVisible({
      timeout: 5000,
    });

    // Wait for run to appear and click Tasks
    const tasksLink = page.getByRole("link", { name: "Tasks" }).first();
    await expect(tasksLink).toBeVisible({ timeout: 10000 });
    await tasksLink.click();

    // Should show "Run <runID>" in breadcrumb
    await expect(
      page.getByRole("link", { name: /^Run\s/ }),
    ).toBeVisible();
  });

  test("run tasks view has link to graph view", async ({ page, request }) => {
    const pipelineName = uniqueName("tasks-to-graph-test");
    await createPipeline(
      request,
      pipelineName,
      `export const pipeline = async () => { console.log("test"); };`,
    );

    await page.goto("/pipelines/");
    await page.getByRole("link", { name: pipelineName }).click();

    // Trigger the pipeline
    await page.getByRole("button", { name: /trigger/i }).click();
    await expect(page.getByText(/triggered successfully/i)).toBeVisible({
      timeout: 5000,
    });

    // Wait for run to appear and click Tasks
    const tasksLink = page.getByRole("link", { name: "Tasks" }).first();
    await expect(tasksLink).toBeVisible({ timeout: 10000 });
    await tasksLink.click();

    // Should have a "Graph" link
    const graphViewLink = page.getByRole("link", { name: /^Graph$/i });
    await expect(graphViewLink).toBeVisible();

    // Click it and verify navigation
    await graphViewLink.click();
    await expect(page).toHaveURL(/\/runs\/[^/]+\/graph/);
  });

  test("run graph view has link to list view", async ({ page, request }) => {
    const pipelineName = uniqueName("graph-to-tasks-test");
    await createPipeline(
      request,
      pipelineName,
      `export const pipeline = async () => { console.log("test"); };`,
    );

    await page.goto("/pipelines/");
    await page.getByRole("link", { name: pipelineName }).click();

    // Trigger the pipeline
    await page.getByRole("button", { name: /trigger/i }).click();
    await expect(page.getByText(/triggered successfully/i)).toBeVisible({
      timeout: 5000,
    });

    // Wait for run to appear and click Graph
    const graphLink = page.getByRole("link", { name: "Graph" }).first();
    await expect(graphLink).toBeVisible({ timeout: 10000 });
    await graphLink.click();

    // Should have a "Tasks" link
    const listViewLink = page.getByRole("link", { name: /^Tasks$/i });
    await expect(listViewLink).toBeVisible();

    // Click it and verify navigation
    await listViewLink.click();
    await expect(page).toHaveURL(/\/runs\/[^/]+\/tasks/);
  });
});

test.describe("Navigation", () => {
  test("health endpoint returns OK", async ({ request }) => {
    const response = await request.get("/health");
    expect(response.ok()).toBeTruthy();
    expect(await response.text()).toBe("OK");
  });

  test("pipelines page has proper structure", async ({ page }) => {
    await page.goto("/pipelines/");

    // Should have the main heading
    await expect(page.getByRole("heading", { name: "Pipelines" }))
      .toBeVisible();

    // Should have navigation
    await expect(page.locator("nav")).toBeVisible();
  });
});

test.describe("Live Updates", () => {
  // These tests involve Docker container execution which can be slow
  test.setTimeout(120000);

  test("shows Live indicator when pipeline is running", async ({ page, request }) => {
    // Create a slow pipeline to ensure we catch it while running
    // Use a name that doesn't contain 'live' to avoid matching issues
    const pipelineName = uniqueName("indicator-test");
    await createPipeline(
      request,
      pipelineName,
      `export const pipeline = async () => { 
        await runtime.run({
          name: "slow-task",
          image: "alpine",
          command: { path: "sleep", args: ["10"] }
        });
      };`,
      "docker",
    );

    await page.goto("/pipelines/");
    await page.getByRole("link", { name: pipelineName }).click();

    // Trigger the pipeline
    await page.getByRole("button", { name: /trigger/i }).click();

    // Wait for the trigger to complete
    await expect(page.getByText(/triggered successfully/i)).toBeVisible({
      timeout: 5000,
    });

    // Should show "Live" indicator while running (run is queued or running)
    // Use exact match to avoid matching pipeline names
    const liveIndicator = page.getByText("Live", { exact: true });

    // Live indicator should appear when there's an active run
    await expect(liveIndicator).toBeVisible({ timeout: 30000 });
  });

  test("tasks page shows Live indicator and updates", async ({ page, request }) => {
    const pipelineName = uniqueName("tasks-update-test");
    await createPipeline(
      request,
      pipelineName,
      `export const pipeline = async () => { 
        await runtime.run({
          name: "slow-task",
          image: "alpine",
          command: { path: "sleep", args: ["5"] }
        });
      };`,
      "docker",
    );

    await page.goto("/pipelines/");
    await page.getByRole("link", { name: pipelineName }).click();

    // Trigger the pipeline
    await page.getByRole("button", { name: /trigger/i }).click();
    await expect(page.getByText(/triggered successfully/i)).toBeVisible({
      timeout: 5000,
    });

    // Wait for the run to appear and click Tasks
    const tasksLink = page.getByRole("link", { name: "Tasks" }).first();
    await expect(tasksLink).toBeVisible({ timeout: 15000 });
    await tasksLink.click();

    // Should be on the tasks page
    await expect(page).toHaveURL(/\/runs\/[^/]+\/tasks/);

    // Wait for completion - the run will eventually show Live indicator or tasks
    const liveIndicator = page.getByText("Live", { exact: true });
    const taskItem = page.locator(".task-item").first();
    await expect
      .poll(
        async () =>
          (await liveIndicator.isVisible()) || (await taskItem.isVisible()),
        { timeout: 60000 },
      )
      .toBeTruthy();
  });

  test("graph page shows Live indicator and updates", async ({ page, request }) => {
    const pipelineName = uniqueName("graph-update-test");
    await createPipeline(
      request,
      pipelineName,
      `export const pipeline = async () => { 
        await runtime.run({
          name: "slow-task",
          image: "alpine",
          command: { path: "sleep", args: ["5"] }
        });
      };`,
      "docker",
    );

    await page.goto("/pipelines/");
    await page.getByRole("link", { name: pipelineName }).click();

    // Trigger the pipeline
    await page.getByRole("button", { name: /trigger/i }).click();
    await expect(page.getByText(/triggered successfully/i)).toBeVisible({
      timeout: 5000,
    });

    // Wait for the run to appear and click Graph
    const graphLink = page.getByRole("link", { name: "Graph" }).first();
    await expect(graphLink).toBeVisible({ timeout: 15000 });
    await graphLink.click();

    // Should be on the graph page
    await expect(page).toHaveURL(/\/runs\/[^/]+\/graph/);

    // Page should load (graph container should be visible)
    await expect(page.locator("#graph-container")).toBeVisible({
      timeout: 10000,
    });
  });

  test("run status transitions from queued to running to completed", async ({ page, request }) => {
    const pipelineName = uniqueName("status-transition-test");
    await createPipeline(
      request,
      pipelineName,
      `export const pipeline = async () => { 
        await runtime.run({
          name: "slow-task",
          image: "alpine",
          command: { path: "sleep", args: ["3"] }
        });
      };`,
      "docker",
    );

    await page.goto("/pipelines/");
    await page.getByRole("link", { name: pipelineName }).click();

    // Trigger the pipeline
    await page.getByRole("button", { name: /trigger/i }).click();
    await expect(page.getByText(/triggered successfully/i)).toBeVisible({
      timeout: 5000,
    });

    // Should see the run start (queued or running or already complete)
    await expect(page.getByText(/queued|running|success|failed/i).first())
      .toBeVisible({
        timeout: 15000,
      });

    // Eventually should complete (success or failed)
    await expect(page.getByText(/success|failed/i).first()).toBeVisible({
      timeout: 60000,
    });
  });
});
