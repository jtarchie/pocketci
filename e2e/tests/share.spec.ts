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
      const r = await request.get(`/api/runs/${runID}/status`);
      const body = await r.json();
      return body.status;
    },
    { timeout: 30000, intervals: [500] },
  ).toMatch(/success|failed/);

  return runID;
}

async function getSharePath(
  request: APIRequestContext,
  runID: string,
): Promise<string> {
  const resp = await request.post(`/api/runs/${runID}/share`);
  expect(resp.ok()).toBeTruthy();
  const { share_path: sharePath } = await resp.json();
  return sharePath;
}

test.describe("Share Feature", () => {
  test.setTimeout(60000);

  test("Share button appears in the ... menu on the tasks page", async ({
    page,
    request,
  }) => {
    const runID = await createAndRunPipeline(request, uniqueName("share-btn"));
    await page.goto(`/runs/${runID}/tasks`);

    await page.getByLabel("More actions").click();

    // The button has role="menuitem" and aria-label="Copy share link for this run"
    await expect(
      page.getByLabel("Copy share link for this run"),
    ).toBeVisible();
  });

  test("Share button copies URL to clipboard and shows toast", async ({
    page,
    context,
    request,
  }) => {
    const runID = await createAndRunPipeline(
      request,
      uniqueName("share-copy"),
    );
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);

    await page.goto(`/runs/${runID}/tasks`);
    await page.getByLabel("More actions").click();
    await page.getByLabel("Copy share link for this run").click();

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
    const runID = await createAndRunPipeline(
      request,
      uniqueName("share-hash"),
    );
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);

    await page.goto(`/runs/${runID}/tasks#someanchor`);
    await page.getByLabel("More actions").click();
    await page.getByLabel("Copy share link for this run").click();

    await expect(page.getByText(/share link copied/i)).toBeVisible({
      timeout: 5000,
    });

    const clipText = await page.evaluate(() =>
      navigator.clipboard.readText()
    );
    expect(clipText).toContain("#someanchor");
  });

  test("shared view hides action buttons", async ({ page, request }) => {
    const runID = await createAndRunPipeline(
      request,
      uniqueName("share-readonly"),
    );
    const sharePath = await getSharePath(request, runID);
    await page.goto(sharePath);

    // No Stop button
    await expect(
      page.getByRole("button", { name: /stop/i }),
    ).not.toBeVisible();

    // No "..." more actions menu
    await expect(page.getByLabel("More actions")).not.toBeVisible();

    // No Share button
    await expect(
      page.getByLabel("Copy share link for this run"),
    ).not.toBeVisible();
  });

  test("shared view shows 'Shared Run' breadcrumb", async ({
    page,
    request,
  }) => {
    const runID = await createAndRunPipeline(
      request,
      uniqueName("share-breadcrumb"),
    );
    const sharePath = await getSharePath(request, runID);
    await page.goto(sharePath);

    await expect(page.getByText(/shared run/i)).toBeVisible();
  });

  test("shared view has data-readonly attribute on tasks container", async ({
    page,
    request,
  }) => {
    const runID = await createAndRunPipeline(
      request,
      uniqueName("share-data-attr"),
    );
    const sharePath = await getSharePath(request, runID);
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
    const runID = await createAndRunPipeline(
      request,
      uniqueName("share-no-readonly"),
    );
    await page.goto(`/runs/${runID}/tasks`);

    const readonly = await page.locator("#tasks-container").getAttribute(
      "data-readonly",
    );
    expect(readonly).toBeNull();
  });

  test("shared view: .term-line-num has pointer-events none", async ({
    page,
    request,
  }) => {
    const runID = await createAndRunPipeline(
      request,
      uniqueName("share-ptr-events"),
    );
    const sharePath = await getSharePath(request, runID);
    await page.goto(sharePath);

    // If no terminal lines exist, the pointer-events style is still applied via
    // a <style> tag in the page — verify that directly.
    const stylePresent = await page.evaluate(() => {
      const sheets = Array.from(document.styleSheets);
      for (const sheet of sheets) {
        try {
          const rules = Array.from(sheet.cssRules || []);
          for (const rule of rules) {
            if (
              rule instanceof CSSStyleRule &&
              rule.selectorText === ".term-line-num" &&
              rule.style.pointerEvents === "none"
            ) {
              return true;
            }
          }
        } catch (_) {
          // cross-origin sheet, skip
        }
      }
      return false;
    });
    expect(stylePresent).toBe(true);
  });

  test("shared view: clicking line number does not update URL hash", async ({
    page,
    request,
  }) => {
    const runID = await createAndRunPipeline(
      request,
      uniqueName("share-no-linesel"),
    );
    const sharePath = await getSharePath(request, runID);
    await page.goto(sharePath);

    // If no term-line elements exist, skip — nothing to click
    const lineCount = await page.locator(".term-line-num").count();
    if (lineCount === 0) {
      test.skip();
      return;
    }

    const hashBefore = await page.evaluate(() => window.location.hash);

    // Click with force to bypass pointer-events:none — the JS handler must
    // still not fire (it's disabled in read-only mode)
    await page.locator(".term-line-num").first().click({ force: true });

    // Give any async hash update a chance to settle
    await page.waitForTimeout(300);
    const hashAfter = await page.evaluate(() => window.location.hash);

    // Hash must not have been updated to an L-anchor by our JS handler
    // (a native anchor jump is acceptable but our range-selection JS should be silent)
    expect(hashAfter).not.toMatch(/^#.+-L\d+-L\d+$/); // range pattern our JS writes
    // If it changed to a single-line anchor from native browser behavior that's
    // fine — we only care the JS selection logic is disabled
    expect(hashBefore).toBe(hashBefore); // self-check; real assertion is above
  });

  test("shared view with hash fragment highlights correct lines", async ({
    page,
    request,
  }) => {
    const runID = await createAndRunPipeline(
      request,
      uniqueName("share-hash-highlight"),
    );
    const sharePath = await getSharePath(request, runID);

    // Visit once to collect a line element ID, if any exist
    await page.goto(sharePath);

    // Open all details so hidden lines become accessible in the DOM
    await page.locator("details.task-item").evaluateAll((els) =>
      els.forEach((el) => el.setAttribute("open", ""))
    );

    const lineCount = await page.locator(".term-line[id]").count();
    if (lineCount === 0) {
      test.skip();
      return;
    }

    const firstLineID = await page.locator(".term-line[id]").first()
      .getAttribute("id");
    if (!firstLineID) {
      test.skip();
      return;
    }

    // Navigate to the share URL with the hash fragment
    await page.goto(`${sharePath}#${firstLineID}`);

    // The targeted line should receive the highlighted class
    await expect(page.locator(`#${CSS.escape(firstLineID)}`)).toHaveClass(
      /highlighted/,
      { timeout: 3000 },
    );
  });

  test("invalid share token returns 404", async ({ page }) => {
    await page.goto("/share/invalid-token-xyz/tasks");
    await expect(page.locator("body")).toContainText(/not found|404/i);
  });

  test("share API returns 404 for nonexistent run", async ({ request }) => {
    const resp = await request.post("/api/runs/nonexistent-run-id/share");
    expect(resp.status()).toBe(404);
  });
});
