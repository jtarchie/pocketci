# MCP Server

The CI server exposes a
[Model Context Protocol (MCP)](https://modelcontextprotocol.io) endpoint at
`/mcp`. This lets AI assistants (GitHub Copilot, Claude, etc.) inspect pipeline
runs, list task output, and search across pipelines directly from your editor —
no manual curl required.

## Quick start

### 1. Start the server

```bash
pocketci server \
  --storage-sqlite-path pocketci.db \
  --basic-auth admin:yourpassword \
  --port 8080
```

### 2. Configure VS Code

Add a `.vscode/mcp.json` file to your workspace:

```jsonc
{
  "servers": {
    "ci": {
      "url": "http://localhost:8080/mcp",
      "type": "http",
      "headers": {
        "Authorization": "Basic ${input:ciBasicAuth}"
      }
    }
  },
  "inputs": [
    {
      "id": "ciBasicAuth",
      "type": "promptString",
      "description": "Base64-encoded 'user:password' (run: echo -n 'admin:yourpassword' | base64)",
      "password": true
    }
  ]
}
```

Generate the Base64 value once and store it securely:

```bash
echo -n 'admin:yourpassword' | base64
```

VS Code will prompt for the value on first use and remember it for the session.

#### With OAuth (Bearer Token)

If the server uses OAuth instead of Basic Auth, pass a Bearer token:

```jsonc
{
  "servers": {
    "ci": {
      "url": "https://ci.example.com/mcp",
      "type": "http",
      "headers": {
        "Authorization": "Bearer ${input:ciToken}"
      }
    }
  },
  "inputs": [
    {
      "id": "ciToken",
      "type": "promptString",
      "description": "PocketCI auth token (from: pocketci login)",
      "password": true
    }
  ]
}
```

Get a token by running `pocketci login -s https://ci.example.com`, then paste
the printed token when VS Code prompts.

### 3. Use the tools

Open GitHub Copilot Chat (or any MCP-compatible assistant) and ask questions
like:

- _"What failed in run `abc123`?"_
- _"Show me the stdout of the last task in this run."_
- _"Find pipelines that use the `busybox` image."_

## Available tools

### `get_run`

Retrieve the status and metadata for a single pipeline run.

**Input**

| Field    | Type   | Description         |
| -------- | ------ | ------------------- |
| `run_id` | string | The run ID to fetch |

**Returns** JSON with `status` (`queued` / `running` / `success` / `failed`),
`pipeline_id`, `started_at`, `finished_at`, and any error message.

**Example prompt**

> "Get the status of run `_Y0_q5n3RMUktK5y2tYGO`."

---

### `list_run_tasks`

List every task that executed within a run, including stdout, stderr, elapsed
time, and exit status.

**Input**

| Field    | Type   | Description                    |
| -------- | ------ | ------------------------------ |
| `run_id` | string | The run ID whose tasks to list |

**Returns** An array of task objects, each with:

| Field        | Description                                 |
| ------------ | ------------------------------------------- |
| `path`       | Hierarchical task identifier                |
| `status`     | `success` / `failed` / `running` / `queued` |
| `type`       | `task`, `agent`, or `pipeline`              |
| `stdout`     | Captured standard output                    |
| `stderr`     | Captured standard error                     |
| `elapsed`    | Wall-clock duration                         |
| `started_at` | RFC3339 start timestamp                     |

**Example prompt**

> "List all tasks for run `_Y0_q5n3RMUktK5y2tYGO` and tell me which one failed."

---

### `get_run_task`

Fetch a single task record within a run and return the full stored payload. Use
this when you need complete output for one task (for example very large
`stdout`, `audit_log`, or `toolCalls`) instead of scanning all tasks.

**Input**

| Field    | Type   | Description                                                                                      |
| -------- | ------ | ------------------------------------------------------------------------------------------------ |
| `run_id` | string | The run ID containing the task                                                                   |
| `path`   | string | Task path, either absolute (`/pipeline/<run_id>/...`) or relative to the run prefix (`jobs/...`) |

`path` must resolve inside the run (`/pipeline/<run_id>/...`) or the tool will
return an error.

**Returns** A one-item array containing `{ path, payload }`, where `payload` is
the complete task object as stored by the server.

**Example prompts**

> "Get the full payload for task
> `/pipeline/_Y0_q5n3RMUktK5y2tYGO/jobs/review-pr/1/agent/code-quality-reviewer`
> in run `_Y0_q5n3RMUktK5y2tYGO`."

> "For run `_Y0_q5n3RMUktK5y2tYGO`, fetch
> `jobs/review-pr/1/agent/code-quality-reviewer` with full output."

---

### `search_tasks`

Full-text search in two modes — either within one run's task output, or across
all runs for a pipeline.

**Input**

| Field         | Type    | Required       | Description                                                                           |
| ------------- | ------- | -------------- | ------------------------------------------------------------------------------------- |
| `run_id`      | string  | one of the two | Search task stdout/stderr within this single run                                      |
| `pipeline_id` | string  | one of the two | Search runs for this pipeline (by ID, status, error) — mirrors the web UI runs search |
| `query`       | string  | yes            | Search query (see mode notes below)                                                   |
| `page`        | integer | no             | 1-based page number (default: 1, `pipeline_id` mode only)                             |
| `per_page`    | integer | no             | Results per page (default: 20, `pipeline_id` mode only)                               |

Provide either `run_id` **or** `pipeline_id`. If `run_id` is given it takes
precedence.

**Mode 1 — search task output within a run (`run_id`)**

Uses FTS5 to match any task's stdout/stderr/text fields. Returns matching task
records (path, status, stdout, stderr). Useful for pinpointing an error message
or stack trace without scrolling through all output.

| FTS5 query       | Matches                     |
| ---------------- | --------------------------- |
| `panic`          | Any task containing "panic" |
| `"exit code 1"`  | Exact phrase                |
| `error NOT warn` | "error" but not "warn"      |

**Mode 2 — search runs for a pipeline (`pipeline_id`)**

Uses FTS5 on run ID, status, and error message. Supports the same prefix
matching and quoted phrases as `run_id` mode. Returns a paginated
`PaginationResult[PipelineRun]`. Equivalent to the pipeline-level runs search in
the web UI (`/pipelines/:id/runs-search/`).

| Query example  | Matches                         |
| -------------- | ------------------------------- |
| `failed`       | Runs with status `failed`       |
| `timeout`      | Runs with "timeout" in error    |
| `"out of mem"` | Exact phrase in error message   |
| `seg`          | Prefix match ("segfault", etc.) |

**Example prompts**

> "Search run `_Y0_q5n3RMUktK5y2tYGO` for `permission denied`." _(run_id mode)_

> "Search pipeline `d22bcae276f280529d3c4c351e81c699` runs for `failed`."
> _(pipeline_id mode)_

---

### `search_pipelines`

Full-text search across all stored pipelines by name or content. Supports
pagination.

**Input**

| Field      | Type    | Description                        |
| ---------- | ------- | ---------------------------------- |
| `query`    | string  | Search string (empty → return all) |
| `page`     | integer | 1-based page number (default: 1)   |
| `per_page` | integer | Results per page (default: 20)     |

**Returns** Paginated list of pipelines with `id`, `name`, and `driver`.

**Example prompt**

> "Find all pipelines that reference `golang` in their source."

## Connecting to a remote server

For a deployed instance (e.g. on Fly.io), point the URL at the public hostname:

```jsonc
{
  "servers": {
    "ci": {
      "url": "https://ci.fly.dev/mcp",
      "type": "http",
      "headers": {
        "Authorization": "Basic ${input:ciBasicAuth}"
      }
    }
  },
  "inputs": [
    {
      "id": "ciBasicAuth",
      "type": "promptString",
      "description": "Base64-encoded 'user:password' for ci.fly.dev",
      "password": true
    }
  ]
}
```

## Typical debug workflow

1. A pipeline run fails. Copy the run ID from the URL (`/runs/<run_id>/tasks`).
2. Ask the assistant: _"What failed in run `<run_id>`?"_
3. The assistant calls `get_run` to confirm the status, then `list_run_tasks` to
   surface the failed step and its stderr.
4. If one step needs deep inspection, call `get_run_task` to retrieve full
   payload fields (including large `stdout`, `audit_log`, and `toolCalls`).
5. If the output is noisy, use `search_tasks` to zero in on the error.
6. Fix the pipeline source and re-run.
