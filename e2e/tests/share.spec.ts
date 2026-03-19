import { type APIRequestContext, expect, test } from "@playwright/test";

function uniqueName(base: string): string {
  return `${base}-${Date.now()}-${Math.random().toString(36).substring(7)}`;
}

async function createAndRunPipeline(
  request: APIRequestContext,
  name: string,
): Promise<string> {
  const createResp = await request.put(
    `/api/pipelines/${encodeURIComponent(name)}`,
    {
      data: {
        content:
          `export const pipeline = async () => { console.log("share-test-output"); };`,
        driver: "native",
      },
    },
  );
  expect(createResp.ok()).toBeTruthy();
  const { id: pipelineID } = await createResp.json();

  const triggerResp = await request.post(
    `/api/pipelines/${encodeURIComponent(pipelineID)}/trigger`,
  );
  expect(triggerResp.ok()).toBeTruthy();
  const { run_id: runID } = await triggerResp.json();

  // Poll until completed
  await expect.poll(
    async () => {
      const r = await request.get(`/api/runs/${runID}`);
      const body = await r.json();
      return body.status;
    },
    { timeout: 30000, intervals: [500] },
  ).toMatch(/success|failed/);

  return runID;
}

test.describe("Share Feature", () => {
  test.setTimeout(60000);

  test("Share button appears in the ... menu on the tasks page", async ({
    page,
    request,
  }) => {
    const name = uniqueName("share-btn");
    const runID = await createAndRunPipeline(request, name);

    await page.goto(`/runs/${runID}/tasks`);

    // Open the "..." dropdown
    await page.getByLabel("More actions").click();

    // Share button should be visible
    await expect(
      page.getByRole("button", { name: /share/i }),
    ).toBeVisible();
  });

  test("Share button copies URL to clipboard and shows toast", async ({
    page,
    context,
    request,
  }) => {
    const name = uniqueName("share-copy");
    const runID = await createAndRunPipeline(request, name);

    // Grant clipboard permissions
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);

    await page.goto(`/runs/${runID}/tasks`);

    // Open the "..." dropdown and click Share
    await page.getByLabel("More actions").click();
    await page.getByRole("button", { name: /share/i }).click();

    // Toast should appear
    await expect(page.getByText(/share link copied/i)).toBeVisible({
      timeout: 5000,
    });

    // Clipboard should contain a share URL
    const clipText = await page.evaluate(() =>
      navigator.clipboard.readText()
    );
    expect(clipText).toContain("/share/");
    expect(clipText).toContain("/tasks");
  });

  test("Share URL preserves hash fragment", async ({
    page,
    context,
    request,
  }) => {
    const name = uniqueName("share-hash");
    const runID = await createAndRunPipeline(request, name);

    await context.grantPermissions(["clipboard-read", "clipboard-write"]);

    // Navigate with a hash fragment
    await page.goto(`/runs/${runID}/tasks#someanchor`);

    await page.getByLabel("More actions").click();
    await page.getByRole("button", { name: /share/i }).click();

    await expect(page.getByText(/share link copied/i)).toBeVisible({
      timeout: 5000,
    });

    const clipText = await page.evaluate(() =>
      navigator.clipboard.readText()
    );
    expect(clipText).toContain("#someanchor");
  });

  test("shared view hides action buttons", async ({ page, request }) => {
    const name = uniqueName("share-readonly");
    const runID = await createAndRunPipeline(request, name);

    // Get a share token via API
    const shareResp = await request.post(`/api/runs/${runID}/share`);
    expect(shareResp.ok()).toBeTruthy();
    const { share_path: sharePath } = await shareResp.json();

    await page.goto(sharePath);

    // No Stop button
    await expect(page.getByRole("button", { name: /stop/i })).not.toBeVisible();

    // No "..." more actions menu (which contains Graph View, JSON links, Share)
    await expect(page.getByLabel("More actions")).not.toBeVisible();

    // No Share button
    await expect(
      page.getByRole("button", { name: /share/i }),
    ).not.toBeVisible();
  });

  test("shared view shows 'Shared Run' breadcrumb", async ({
    page,
    request,
  }) => {
    const name = uniqueName("share-breadcrumb");
    const runID = await createAndRunPipeline(request, name);

    const shareResp = await request.post(`/api/runs/${runID}/share`);
    const { share_path: sharePath } = await shareResp.json();

    await page.goto(sharePath);

    await expect(page.getByText(/shared run/i)).toBeVisible();
  });

  test("shared view has data-readonly attribute on tasks container", async ({
    page,
    request,
  }) => {
    const name = uniqueName("share-data-attr");
    const runID = await createAndRunPipeline(request, name);

    const shareResp = await request.post(`/api/runs/${runID}/share`);
    const { share_path: sharePath } = await shareResp.json();

    await page.goto(sharePath);

    const readonly = await page.locator("#tasks-container").getAttribute(
      "data-readonly",
    );
    expect(readonly).toBe("true");
  });

  test("normal tasks view does NOT have data-readonly", async ({
    page,
    request,
  }) => {
    const name = uniqueName("share-no-readonly");
    const runID = await createAndRunPipeline(request, name);

    await page.goto(`/runs/${runID}/tasks`);

    const readonly = await page.locator("#tasks-container").getAttribute(
      "data-readonly",
    );
    expect(readonly).toBeNull();
  });

  test("shared view clicking line number does not update URL hash", async ({
    page,
    request,
  }) => {
    const name = uniqueName("share-no-linesel");
    const runID = await createAndRunPipeline(request, name);

    const shareResp = await request.post(`/api/runs/${runID}/share`);
    const { share_path: sharePath } = await shareResp.json();

    await page.goto(sharePath);

    // Find the first line number link and click it
    const firstLineNum = page.locator(".term-line-num").first();
    // term-line-num has pointer-events:none, so clicks pass through; either way
    // the hash must not change to an L-anchor pattern
    const hashBefore = await page.evaluate(() => window.location.hash);
    await firstLineNum.click({ force: true });
    const hashAfter = await page.evaluate(() => window.location.hash);

    // Hash should not have changed to a line anchor (L\d+ pattern)
    expect(hashAfter).not.toMatch(/-L\d+/);
    // And it shouldn't have changed from before
    expect(hashAfter).toBe(hashBefore);
  });

  test("shared view with hash fragment highlights correct lines", async ({
    page,
    request,
  }) => {
    const name = uniqueName("share-hash-highlight");
    const runID = await createAndRunPipeline(request, name);

    const shareResp = await request.post(`/api/runs/${runID}/share`);
    const { share_path: sharePath } = await shareResp.json();

    // Get the first line ID from the page first
    await page.goto(sharePath);
    const firstLineID = await page.locator(".term-line").first().getAttribute(
      "id",
    );
    if (!firstLineID) {
      test.skip();
      return;
    }

    // Now navigate with a hash pointing to that line
    await page.goto(`${sharePath}#${firstLineID}`);

    // The line should have the highlighted class
    await expect(page.locator(`#${firstLineID}`)).toHaveClass(/highlighted/, {
      timeout: 3000,
    });
  });

  test("invalid share token returns 404", async ({ page }) => {
    await page.goto("/share/invalid-token-xyz/tasks");
    await expect(page).toHaveURL(/\/share\/invalid-token-xyz\/tasks/);
    // Should show a 404 / error response
    await expect(page.locator("body")).toContainText(/not found|404/i);
  });

  test("share API returns 404 for nonexistent run", async ({ request }) => {
    const resp = await request.post("/api/runs/nonexistent-run-id/share");
    expect(resp.status()).toBe(404);
  });
});
