// ── Types ─────────────────────────────────────────────────────────────────
interface Issue {
  severity?: unknown;
  description?: unknown;
  file?: unknown;
  line?: unknown;
  start_line?: unknown;
}
type Severity = "critical" | "high" | "medium" | "low";
interface Review { summary?: unknown; issues?: unknown[] }
interface InlineComment {
  path: string; line: number; side: "RIGHT"; body: string;
  start_line?: number; start_side?: "RIGHT";
}
interface FallbackFinding {
  severity: Severity;
  description: string;
  file?: string;
  line?: number;
}

// ── Helpers ───────────────────────────────────────────────────────────────

// Extract JSON from agent .text, tolerating preamble and ```json fences.
function parseReview(text: string): Review {
  const fenceMatch = text.match(/```json\s*([\s\S]*?)\s*```/i);
  if (fenceMatch) return JSON.parse(fenceMatch[1]) as Review;
  return JSON.parse(text.trim()) as Review;
}

function normalizeSeverity(value: unknown): Severity {
  switch (String(value ?? "low").toLowerCase()) {
    case "critical":
    case "high":
    case "medium":
    case "low":
      return String(value ?? "low").toLowerCase() as Severity;
    default:
      return "low";
  }
}

// Build a Set<"path:line"> for every added line in the unified diff.
function changedLines(diff: string): Set<string> {
  const changed = new Set<string>();
  let file = "";
  let line = 0;
  for (const raw of diff.split("\n")) {
    if (raw.startsWith("diff --git ")) {
      // Header is always "diff --git a/<path> b/<path>".
      // Slicing after the last " b/" gives the new-file path,
      // which is what the LLM reports and what GitHub expects.
      const bIdx = raw.lastIndexOf(" b/");
      file = bIdx !== -1 ? raw.slice(bIdx + 3) : "";
    } else if (raw.startsWith("@@ ")) {
      const m = raw.match(/\+(\d+)/);
      line = m ? Number(m[1]) - 1 : 0;
    } else if (raw.startsWith("--- ") || raw.startsWith("+++ ")) {
      // skip diff header lines
    } else if (raw.startsWith("+")) {
      // Added line — anchorable on RIGHT side.
      line++;
      if (file) changed.add(`${file}:${line}`);
    } else if (raw.startsWith(" ")) {
      // Context line — also visible in the diff and anchorable.
      // LLMs frequently cite unchanged lines that frame an issue.
      line++;
      if (file) changed.add(`${file}:${line}`);
    }
    // "-" deleted lines don't advance the new-file counter
    // and cannot be commented on the RIGHT side.
  }
  return changed;
}

// Thin wrapper around the GitHub REST API using native fetch.
async function github(
  path: string,
  opts: RequestInit = {},
): Promise<unknown> {
  const res = await fetch(`https://api.github.com/${path}`, {
    ...opts,
    headers: {
      Authorization: `Bearer ${GH_TOKEN}`,
      Accept: "application/vnd.github+json",
      "X-GitHub-Api-Version": "2022-11-28",
      "Content-Type": "application/json",
      ...(opts.headers ?? {}),
    },
  });
  if (!res.ok) {
    throw new Error(
      `GitHub API ${path} → ${res.status} ${res.statusText}\n${await res.text()}`,
    );
  }
  return res.json();
}

// ── Main ─────────────────────────────────────────────────────────────────

const GH_TOKEN = Deno.env.get("GH_TOKEN") ?? "";
const GH_REPO = Deno.env.get("GH_REPO") ?? "";
const PR_NUMBER = Deno.env.get("PR_NUMBER") ?? "";

// 1. Read & validate agent output
const agentResult = JSON.parse(
  await Deno.readTextFile("final-review/result.json"),
) as { text?: string };

if (!agentResult.text) {
  console.error("error: .text missing from final-review/result.json");
  Deno.exit(1);
}

let review: Review;
try {
  review = parseReview(agentResult.text);
} catch (e) {
  console.error("error: final-reviewer output is not valid JSON:", e);
  console.error(agentResult.text);
  Deno.exit(1);
}

if (typeof review.summary !== "string" || !Array.isArray(review.issues)) {
  console.error(
    "error: review JSON must have summary (string) and issues (array)",
  );
  Deno.exit(1);
}

// 2. Build changed-line index from the diff
const diff = await Deno.readTextFile("diff/pr.diff");
const changed = changedLines(diff);

// 3. Classify each issue as inline-anchorable or fallback
const summary = review.summary || "Automated review completed.";
const inline: InlineComment[] = [];
const fallback: FallbackFinding[] = [];

for (const raw of (review.issues as Issue[])) {
  const severity = normalizeSeverity(raw.severity);
  const description = String(raw.description ?? "").trim();
  const file = String(raw.file ?? "").trim();
  const line = typeof raw.line === "number"
    ? raw.line
    : Number(raw.line ?? 0);
  const startLine = raw.start_line != null
    ? (typeof raw.start_line === "number" ? raw.start_line : Number(raw.start_line))
    : 0;

  if (!description) continue;

  if (file && line > 0 && changed.has(`${file}:${line}`)) {
    const comment: InlineComment = {
      path: file,
      line,
      side: "RIGHT",
      body: `[${severity.toUpperCase()}] ${description}`,
    };
    // Attach range fields when start_line is valid and also anchorable in the diff.
    if (startLine > 0 && startLine < line && changed.has(`${file}:${startLine}`)) {
      comment.start_line = startLine;
      comment.start_side = "RIGHT";
    }
    inline.push(comment);
  } else {
    fallback.push({ severity, description, file, line });
  }
}

// 4. Fetch head commit SHA (needed by the review API)
const pr = await github(
  `repos/${GH_REPO}/pulls/${PR_NUMBER}`,
) as { head: { sha: string } };

// 5. Post one batched inline review (or fall back to a plain comment)
if (inline.length > 0) {
  await github(`repos/${GH_REPO}/pulls/${PR_NUMBER}/reviews`, {
    method: "POST",
    body: JSON.stringify({
      commit_id: pr.head.sha,
      event: "COMMENT",
      body: summary,
      comments: inline,
    }),
  });
} else {
  await github(`repos/${GH_REPO}/issues/${PR_NUMBER}/comments`, {
    method: "POST",
    body: JSON.stringify({ body: summary }),
  });
}

// 6. Post one extra comment for issues that couldn't be anchored
if (fallback.length > 0) {
  const linkablePath = (path: string): string => path
    .split("/")
    .map((part) => encodeURIComponent(part))
    .join("/");

  const body = [
    "## Automated Review: Unanchored Findings",
    "",
    "> [!NOTE]",
    "> These findings could not be attached inline to the current diff:",
    "> Locations below are provided for convenience, but are not anchored to the patch.",
    "",
    ...fallback.map(
      (f) => {
        const hasLocation = Boolean(f.file) && Boolean(f.line && f.line > 0);
        const location = hasLocation
          ? `[${f.file}:${f.line}](https://github.com/${GH_REPO}/blob/${pr.head.sha}/${linkablePath(f.file as string)}#L${f.line})`
          : "location unavailable";
        return `- [${f.severity.toUpperCase()}] ${location} - ${f.description}`;
      },
    ),
  ].join("\n");

  await github(`repos/${GH_REPO}/issues/${PR_NUMBER}/comments`, {
    method: "POST",
    body: JSON.stringify({ body }),
  });
}

console.log(`posted inline comments : ${inline.length}`);
console.log(`posted fallback comments: ${fallback.length}`);
