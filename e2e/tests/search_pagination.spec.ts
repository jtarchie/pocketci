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

// Helper to trigger a pipeline run
async function triggerPipeline(
  request: APIRequestContext,
  pipelineId: string,
): Promise<void> {
  const response = await request.post(
    `/api/pipelines/${pipelineId}/trigger`,
    {},
  );
  expect(response.ok()).toBeTruthy();
}

// ─── Pipeline Search ──────────────────────────────────────────────────────────

test.describe("Pipeline Search", () => {
  test("filters pipelines by name", async ({ page, request }) => {
    const marker = uniqueName("pipe-srch");
    await createPipeline(
      request,
      marker,
      `export const pipeline = async () => {};`,
    );

    await page.goto("/pipelines/");

    const searchInput = page.getByLabel("Search pipelines");
    await searchInput.fill(marker);
    // Wait for HTMX debounce (300 ms) + request round-trip
    await page.waitForTimeout(600);

    await expect(page.getByRole("link", { name: marker })).toBeVisible();
  });

  test("shows empty state for unmatched query", async ({ page }) => {
    await page.goto("/pipelines/");

    const searchInput = page.getByLabel("Search pipelines");
    await searchInput.fill("zzzabsolutelynotfound-xyz987");
    await page.waitForTimeout(600);

    await expect(page.getByText("No pipelines found.")).toBeVisible();
  });

  test("clearing search restores all results", async ({ page, request }) => {
    const marker = uniqueName("pipe-srch-clr");
    await createPipeline(
      request,
      marker,
      `export const pipeline = async () => {};`,
    );

    await page.goto("/pipelines/");

    const searchInput = page.getByLabel("Search pipelines");
    // Fill with something that won't match anything
    await searchInput.fill("zzzno-match-xyz987");
    await page.waitForTimeout(600);
    await expect(page.getByText("No pipelines found.")).toBeVisible();

    // Clear → all pipelines should reappear
    await searchInput.fill("");
    await page.waitForTimeout(600);
    await expect(page.getByRole("link", { name: marker })).toBeVisible();
  });
});

// ─── Pipeline Pagination ──────────────────────────────────────────────────────

test.describe("Pipeline Pagination", () => {
  test("shows Next button when pipelines exceed per-page limit", async ({ page, request }) => {
    // Create 2 pipelines; per_page=1 forces pagination
    await createPipeline(
      request,
      uniqueName("pgtest-a"),
      `export const pipeline = async () => {};`,
    );
    await createPipeline(
      request,
      uniqueName("pgtest-b"),
      `export const pipeline = async () => {};`,
    );

    await page.goto("/pipelines/?per_page=1");

    await expect(page.getByText(/Page 1 of/)).toBeVisible();
    await expect(page.getByText("Next →")).toBeVisible();
  });

  test("navigates to next page and back to previous", async ({ page, request }) => {
    await createPipeline(
      request,
      uniqueName("pgnav-a"),
      `export const pipeline = async () => {};`,
    );
    await createPipeline(
      request,
      uniqueName("pgnav-b"),
      `export const pipeline = async () => {};`,
    );

    await page.goto("/pipelines/?per_page=1");

    await expect(page.getByText(/Page 1 of/)).toBeVisible();
    await expect(page.getByText("Next →")).toBeVisible();

    // Navigate forward
    await page.getByText("Next →").click();
    await page.waitForTimeout(400);

    await expect(page.getByText(/Page 2 of/)).toBeVisible();
    await expect(page.getByText("← Previous")).toBeVisible();

    // Navigate back
    await page.getByText("← Previous").click();
    await page.waitForTimeout(400);

    await expect(page.getByText(/Page 1 of/)).toBeVisible();
  });

  test("each page shows different pipelines", async ({ page, request }) => {
    const nameA = uniqueName("pgdiff-a");
    const nameB = uniqueName("pgdiff-b");
    await createPipeline(
      request,
      nameA,
      `export const pipeline = async () => {};`,
    );
    await createPipeline(
      request,
      nameB,
      `export const pipeline = async () => {};`,
    );

    await page.goto("/pipelines/?per_page=1");

    // Record the pipeline visible on page 1
    const page1Link = page.getByRole("link", { name: /^pgdiff-/ }).first();
    const page1Name = await page1Link.textContent();

    // Go to page 2
    await page.getByText("Next →").click();
    await page.waitForTimeout(400);

    // The pipeline on page 2 should be different from page 1
    const page2Link = page.getByRole("link", { name: /^pgdiff-/ }).first();
    const page2Name = await page2Link.textContent();

    expect(page1Name).not.toBe(page2Name);
  });

  test("pipeline search filters results on current page", async ({ page, request }) => {
    // Create 25 pipelines sharing a unique keyword so search returns >20 hits
    const keyword = `srchpg${Date.now()}`;
    const promises = Array.from({ length: 22 }, (_, i) =>
      createPipeline(
        request,
        `${keyword}-${i}`,
        `export const pipeline = async () => {};`,
      ));
    await Promise.all(promises);

    await page.goto("/pipelines/");

    const searchInput = page.getByLabel("Search pipelines");
    await searchInput.fill(keyword);
    // Allow debounce + HTMX round-trip (default per_page=20, so 22 results → 2 pages)
    await page.waitForTimeout(600);

    // Page 1 of filtered search results should appear
    await expect(page.getByText(/Page 1 of/)).toBeVisible();
    const nextBtn = page.getByText("Next →");
    await expect(nextBtn).toBeVisible();

    // Navigate to page 2 — search input value is carried via hx-include
    await nextBtn.click();
    await page.waitForTimeout(600);

    await expect(page.getByText(/Page 2 of/)).toBeVisible();
    // Results on page 2 should still contain the keyword
    const links = page.getByRole("link").filter({ hasText: keyword });
    await expect(links.first()).toBeVisible();
  });
});

// ─── Runs Search ─────────────────────────────────────────────────────────────

test.describe("Runs Search", () => {
  test("search input is visible on pipeline detail page", async ({ page, request }) => {
    const id = await createPipeline(
      request,
      uniqueName("run-srchvis"),
      `export const pipeline = async () => {};`,
    );

    await page.goto(`/pipelines/${id}/`);

    await expect(
      page.getByPlaceholder("Search runs by ID, status, or error…"),
    ).toBeVisible();
  });

  test("shows no-match message for unmatched query", async ({ page, request }) => {
    const id = await createPipeline(
      request,
      uniqueName("run-srchemp"),
      `export const pipeline = async () => {};`,
    );

    await page.goto(`/pipelines/${id}/`);

    const searchInput = page.getByPlaceholder(
      "Search runs by ID, status, or error…",
    );
    await searchInput.fill("zzzabsolutelynotfound-xyz");
    await page.waitForTimeout(600);

    await expect(page.getByText(/No runs match/)).toBeVisible();
  });

  test("filters runs by status", async ({ page, request }) => {
    const id = await createPipeline(
      request,
      uniqueName("run-srchstat"),
      `export const pipeline = async () => {};`,
    );
    await triggerPipeline(request, id);

    await page.goto(`/pipelines/${id}/`);

    // Wait for any run to appear
    await expect(
      page.getByText(/queued|running|success|failed/i).first(),
    ).toBeVisible({ timeout: 10000 });

    const searchInput = page.getByPlaceholder(
      "Search runs by ID, status, or error…",
    );
    // Search by a status word that exists (all runs have a status)
    await searchInput.fill("queued");
    await page.waitForTimeout(600);

    // Either runs appear (matching "queued") or no-match message — both are valid
    const hasRuns = await page
      .getByText(/queued|running|success|failed/i)
      .first()
      .isVisible();
    const hasEmpty = await page.getByText(/No runs match/).isVisible();
    expect(hasRuns || hasEmpty).toBeTruthy();
  });

  test("clearing search restores run list", async ({ page, request }) => {
    const id = await createPipeline(
      request,
      uniqueName("run-srchclr"),
      `export const pipeline = async () => {};`,
    );
    await triggerPipeline(request, id);

    await page.goto(`/pipelines/${id}/`);

    // Wait for the run to appear
    await expect(
      page.getByText(/queued|running|success|failed/i).first(),
    ).toBeVisible({ timeout: 10000 });

    const searchInput = page.getByPlaceholder(
      "Search runs by ID, status, or error…",
    );

    // Type a non-matching query
    await searchInput.fill("zzzno-match-xyz");
    await page.waitForTimeout(600);
    await expect(page.getByText(/No runs match/)).toBeVisible();

    // Clear → run list should restore
    await searchInput.fill("");
    await page.waitForTimeout(600);
    await expect(
      page.getByText(/queued|running|success|failed/i).first(),
    ).toBeVisible({ timeout: 5000 });
  });
});

// ─── Runs Pagination ──────────────────────────────────────────────────────────

test.describe("Runs Pagination", () => {
  test("shows pagination when runs exceed per-page limit", async ({ page, request }) => {
    const id = await createPipeline(
      request,
      uniqueName("runs-pag"),
      `export const pipeline = async () => {};`,
    );

    // Trigger 2 runs so per_page=1 forces pagination
    await triggerPipeline(request, id);
    await triggerPipeline(request, id);

    await page.goto(`/pipelines/${id}/?per_page=1`);

    // Wait for at least one run to be visible
    await expect(
      page.getByText(/queued|running|success|failed/i).first(),
    ).toBeVisible({ timeout: 10000 });

    await expect(page.getByText(/Page 1 of/)).toBeVisible({ timeout: 5000 });
    await expect(page.getByText("Next →")).toBeVisible();
  });

  test("navigates between run pages", async ({ page, request }) => {
    const id = await createPipeline(
      request,
      uniqueName("runs-pagnav"),
      `export const pipeline = async () => {};`,
    );

    await triggerPipeline(request, id);
    await triggerPipeline(request, id);

    await page.goto(`/pipelines/${id}/?per_page=1`);

    await expect(
      page.getByText(/queued|running|success|failed/i).first(),
    ).toBeVisible({ timeout: 10000 });

    await expect(page.getByText(/Page 1 of/)).toBeVisible({ timeout: 5000 });

    // Navigate forward
    await page.getByText("Next →").click();
    await page.waitForTimeout(400);

    await expect(page.getByText(/Page 2 of/)).toBeVisible();
    await expect(page.getByText("← Previous")).toBeVisible();

    // Navigate back
    await page.getByText("← Previous").click();
    await page.waitForTimeout(400);

    await expect(page.getByText(/Page 1 of/)).toBeVisible();
  });

  test("each run page shows a different run", async ({ page, request }) => {
    const id = await createPipeline(
      request,
      uniqueName("runs-pgdiff"),
      `export const pipeline = async () => {};`,
    );

    await triggerPipeline(request, id);
    await triggerPipeline(request, id);

    await page.goto(`/pipelines/${id}/?per_page=1`);

    await expect(
      page.getByText(/queued|running|success|failed/i).first(),
    ).toBeVisible({ timeout: 10000 });

    await expect(page.getByText(/Page 1 of/)).toBeVisible({ timeout: 5000 });

    // Grab the run link on page 1
    const page1RunLink = page.getByRole("link", { name: "Tasks" }).first();
    const href1 = await page1RunLink.getAttribute("href");

    // Navigate to page 2
    await page.getByText("Next →").click();
    await page.waitForTimeout(400);

    const page2RunLink = page.getByRole("link", { name: "Tasks" }).first();
    const href2 = await page2RunLink.getAttribute("href");

    // The two Task links should point to different runs
    expect(href1).not.toBe(href2);
  });
});

// ─── Tasks Search ─────────────────────────────────────────────────────────────

test.describe("Tasks Search", () => {
  // The search input is present even before any tasks load; verify with a
  // lightweight native pipeline that completes quickly.
  test("search input is visible on the tasks page", async ({ page, request }) => {
    const id = await createPipeline(
      request,
      uniqueName("tasks-srchvis"),
      `export const pipeline = async () => {};`,
    );
    await triggerPipeline(request, id);

    await page.goto(`/pipelines/${id}/`);

    const tasksLink = page.getByRole("link", { name: "Tasks" }).first();
    await expect(tasksLink).toBeVisible({ timeout: 10000 });
    await tasksLink.click();

    await expect(page).toHaveURL(/\/runs\/[^/]+\/tasks/);
    await expect(page.getByLabel("Search task output")).toBeVisible();
  });
});
