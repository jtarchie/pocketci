# agent.run()

Run an LLM agent that can use a sandboxed container as a tool.

The agent receives a prompt, calls an LLM, and iteratively invokes built-in
tools backed by the container image you provide. Results stream back in real
time.

```typescript
const result = await agent.run(options);
```

## Options

### Required

| Field    | Type   | Description                                                          |
| -------- | ------ | -------------------------------------------------------------------- |
| `name`   | string | Unique task/agent name                                               |
| `prompt` | string | Initial user message or instruction                                  |
| `model`  | string | Model specifier: `provider/model-name` (see [Providers](#providers)) |
| `image`  | string | Container image for the sandbox (e.g., `"alpine"`, `"ubuntu:22.04"`) |

### Optional

| Field              | Type   | Description                                                                         |
| ------------------ | ------ | ----------------------------------------------------------------------------------- |
| `mounts`           | object | Volume mounts: `{ "name": volumeHandle }`                                           |
| `outputVolumePath` | string | Path inside the container to write `result.json`                                    |
| `llm`              | object | LLM generation overrides (see [LLM Config](#llm))                                   |
| `thinking`         | object | Extended thinking config (see [Thinking](#thinking))                                |
| `safety`           | object | Safety filter overrides (see [Safety](#safety))                                     |
| `contextGuard`     | object | Context window management (see [Context Guard](#context-guard))                     |
| `limits`           | object | Hard turn/token limits (see [Limits](#limits))                                      |
| `context`          | object | Pre-inject prior task outputs into session (see [Context](#context))                |
| `validation`       | object | Output validation via Expr expressions (see [Validation](#validation))              |
| `outputSchema`     | object | JSON schema for structured output (see [Output Schema](#output-schema))             |
| `toolTimeout`      | string | Per-tool timeout duration, e.g. `"60s"`, `"5m"` (see [Tool Timeout](#tool-timeout)) |
| `tools`            | array  | Agent + task tools callable by the LLM (see [Tools](#tools))                        |
| `memory`           | object | Cross-run memory (see [Agent Memory](/runtime/runtime-agent-memory))                |

## Providers

The `model` field uses the format `provider/model-name`. Values after the first
`/` are passed verbatim to the provider.

| Provider     | `model` prefix   | Auth env var         | Notes                             |
| ------------ | ---------------- | -------------------- | --------------------------------- |
| `anthropic`  | `anthropic/...`  | `ANTHROPIC_API_KEY`  | Direct Anthropic API              |
| `openai`     | `openai/...`     | `OPENAI_API_KEY`     | Direct OpenAI API                 |
| `openrouter` | `openrouter/...` | `OPENROUTER_API_KEY` | Proxies 200+ models               |
| `ollama`     | `ollama/...`     | _(none)_             | Local Ollama at `localhost:11434` |

**API key resolution order** (first match wins):

1. Pipeline-scoped secret `agent/<provider>`
2. Global-scoped secret `agent/<provider>`
3. Environment variable `{PROVIDER}_API_KEY`

## LLM Config {#llm}

Fine-tune generation parameters. All fields are optional; omitting a field uses
the provider's built-in default.

```yaml
llm:
  temperature: 0.2 # float, 0.0–2.0 (default: provider default, typically 1.0)
  max_tokens: 8192 # int, max output tokens (default: provider default)
```

```typescript
llm: {
  temperature?: number;  // 0.0–2.0; lower = more deterministic
  max_tokens?: number;   // caps output length; provider default if omitted
}
```

> **Note for Anthropic with `thinking`:** `max_tokens` must be greater than the
> thinking budget. If you set a `thinking.budget` without `max_tokens`, the
> runtime defaults `max_tokens` to `8192`.

## Thinking {#thinking}

Enable extended reasoning / chain-of-thought for supported models. The extra
thinking tokens are billed but not included in `result.text`.

```yaml
thinking:
  budget: 10000 # int >= 1024, required when this block is present
  level: medium # string, Gemini only; omit for Anthropic
```

```typescript
thinking: {
  budget: number;  // thinking token budget (minimum 1024)
  level?: "low" | "medium" | "high" | "minimal"; // Gemini only
}
```

| Field    | Provider support | Notes                                              |
| -------- | ---------------- | -------------------------------------------------- |
| `budget` | All              | Anthropic: maps to `ThinkingBudgetTokens`          |
| `level`  | Gemini only      | Ignored for Anthropic; controls depth of reasoning |

## Safety {#safety}

Override per-category safety filters. Keys are harm category names
(case-insensitive); values are threshold names.

```yaml
safety:
  harassment: block_none
  dangerous_content: block_none
```

```typescript
safety?: {
  [category: string]: string;
};
```

### Category names

| YAML key            | Maps to                           |
| ------------------- | --------------------------------- |
| `harassment`        | `HARM_CATEGORY_HARASSMENT`        |
| `hate_speech`       | `HARM_CATEGORY_HATE_SPEECH`       |
| `sexually_explicit` | `HARM_CATEGORY_SEXUALLY_EXPLICIT` |
| `dangerous_content` | `HARM_CATEGORY_DANGEROUS_CONTENT` |
| `civic_integrity`   | `HARM_CATEGORY_CIVIC_INTEGRITY`   |

### Threshold values

| YAML value               | Effect                                 |
| ------------------------ | -------------------------------------- |
| `block_none`             | Allow all content                      |
| `block_only_high`        | Block only high-confidence harm        |
| `block_medium_and_above` | Block medium + high (provider default) |
| `block_low_and_above`    | Block low, medium, and high            |
| `off`                    | Disable the filter entirely            |

Safety settings are applied to Gemini and OpenAI-compatible models. Anthropic
manages safety at the API level and ignores this field.

## Context Guard {#context-guard}

Automatically manage the context window to prevent token-limit errors on long
agent runs.

```yaml
context_guard:
  strategy: threshold # "threshold" or "sliding_window"
  max_tokens: 100000 # for threshold strategy (default: 128000)
  max_turns: 30 # for sliding_window strategy (default: 30)
```

```typescript
contextGuard?: {
  strategy: "threshold" | "sliding_window";
  max_tokens?: number;  // threshold: evict history when total exceeds this
  max_turns?: number;   // sliding_window: keep only the last N turns
};
```

| Strategy         | `max_tokens` default | `max_turns` default | Behaviour                                            |
| ---------------- | -------------------- | ------------------- | ---------------------------------------------------- |
| `threshold`      | 128000               | _N/A_               | Truncates history once total tokens exceed the limit |
| `sliding_window` | _N/A_                | 30                  | Keeps only the most-recent N conversation turns      |

**Strategy inference:** If `contextGuard` is provided with only `max_turns` (no
`strategy`), the strategy automatically becomes `"sliding_window"`. If only
`max_tokens` is provided, the strategy becomes `"threshold"`. An error is
returned if an invalid `strategy` is explicitly specified.

Omitting `contextGuard` entirely disables context management; the full
conversation history is sent to the model on every turn.

## Progressive Persistence {#progressive-persistence}

Agent runs write results **incrementally** as the agent executes, not just when
finished. This ensures visibility into progress and durability against crashes:

- **Usage metrics** (token counts, LLM requests, tool calls): written to the
  task storage key after every LLM turn. The UI task tree automatically polls
  and displays live token/tool counts while the agent is running.
- **Audit log**: each event is appended to a dedicated storage namespace
  (`/agent-audit/{runID}/...`) as it occurs, before the next LLM call. Events
  are not returned in the web UI (they're for post-run analysis), but they are
  stored immediately to prevent data loss if the agent crashes.

This behavior is transparent to pipeline code. The `result` returned by
`agent.run()` still includes the complete `auditLog` array in memory.

## Built-in Tools {#built-in-tools}

Every agent run has these tools available automatically — no configuration
required.

| Tool              | Description                                                                                    |
| ----------------- | ---------------------------------------------------------------------------------------------- |
| `run_script`      | Run a multi-line shell script inside the sandbox container                                     |
| `read_file`       | Read file contents with optional line `offset` and `limit` (default: 2 000 lines)              |
| `grep`            | Search file contents with regex patterns. Supports `glob_filter` and `max_results`             |
| `glob`            | Find files by name pattern (e.g. `**/*.go`). Returns matching paths sorted                     |
| `write_file`      | Create or overwrite a file. Reads previous content first (truncated to 4 KB) for agent context |
| `list_tasks`      | List all tasks in the current pipeline run with their status and timing                        |
| `get_task_result` | Fetch the stdout, stderr, and exit code for a specific task by name                            |

`list_tasks` is **always pre-fetched** and injected into the session before the
agent's first turn, so the agent knows the run state immediately without
spending a tool-call round-trip on orientation.

`get_task_result` supports fuzzy name matching — a partial or approximate task
name is fine. Byte-length truncation is applied when the output is large
(default 4 096 bytes; override with `max_bytes` in the tool call or via
[`context.max_bytes`](#context)).

## Limits {#limits}

Hard limits that stop agent execution to prevent runaway agents.

```yaml
limits:
  max_turns: 50 # max LLM round-trips (default: 50)
  max_total_tokens: 0 # total token budget; 0 = unlimited
```

```typescript
limits?: {
  max_turns?: number;        // default: 50
  max_total_tokens?: number; // 0 or omitted = unlimited
};
```

A warning is emitted 2 turns before the limit is reached. When the limit is hit,
the agent is stopped and `result.status` is `"limit_exceeded"`.

## Validation {#validation}

Validate the agent's final text output using an [Expr](https://expr-lang.org/)
boolean expression. If the expression returns `false`, a follow-up prompt is
sent asking the model to correct its output.

```yaml
validation:
  expr: 'text != "" && text contains "summary"'
  prompt: >-
    Output valid JSON with a "summary" field and an "issues" array.
```

```typescript
validation?: {
  expr: string;    // Expr boolean expression; env: { text, status }
  prompt?: string; // custom follow-up message on failure
};
```

The expression environment provides `text` (the agent's output) and `status`
(`"success"`). If `prompt` is omitted, a generic follow-up is used.

## Output Schema {#output-schema}

Request structured JSON output from the agent. The schema is included in the
system prompt and **validated** after the agent finishes. If the output does not
conform, a follow-up turn is sent asking the agent to fix its response.

```yaml
output_schema:
  summary: string
  issues[]:
    severity: critical|high|medium|low
    description: string
    file?: string
    line?: int
```

### Compact DSL

| Syntax            | Meaning                             |
| ----------------- | ----------------------------------- |
| `"string"`        | Required string field               |
| `"int"`           | Required integer field              |
| `"number"`        | Required number (float) field       |
| `"bool"`          | Required boolean field              |
| `"a\|b\|c"`       | Required string enum                |
| `"field?"`        | Optional field (suffix `?` on key)  |
| `"field[]"`       | Required array (suffix `[]` on key) |
| `{ nested: ... }` | Nested object                       |

Schema validation checks: JSON parse, required fields, types, enums, array
items, and nested objects. If validation fails, the error message is sent to the
agent so it can retry.

## Tool Timeout {#tool-timeout}

Set a per-tool execution timeout for all sandbox-backed tools. If a tool exceeds
the timeout, an error is returned to the agent so it can adjust its approach
(e.g., break a large operation into smaller steps).

```yaml
tool_timeout: "120s" # applies to all tools except run_script
```

```typescript
toolTimeout?: string; // Go duration format: "60s", "5m", etc.
```

| Tool            | Default timeout    |
| --------------- | ------------------ |
| `run_script`    | 5 minutes (`300s`) |
| All other tools | 1 minute (`60s`)   |

When `tool_timeout` is set, it overrides the default for **all** tools
(including `run_script`). If you need long-running scripts but fast tool
timeouts, prefer leaving `tool_timeout` unset and relying on the defaults.

## Context {#context}

Pre-fetch selected task outputs into the agent's session history before the
first turn. This saves the agent from calling `get_task_result` explicitly for
outputs it is likely to need.

```yaml
context:
  max_bytes: 8192 # max bytes per field; default 4096
  tasks:
    - name: build # fuzzy-matched against task names in the run
      field: stdout # "stdout" | "stderr" | "both" (default: "both")
    - name: lint
```

```typescript
context?: {
  max_bytes?: number;           // truncation limit per field (default 4096)
  tasks?: Array<{
    name: string;               // task name (fuzzy matched)
    field?: "stdout" | "stderr" | "both"; // which field(s) to include
  }>;
};
```

Each entry is injected as a synthetic `get_task_result` tool-call/response pair.
The agent sees the output as if it had already called the tool, and the
[audit log](#audit-log) records these under `type: "pre_context"`.

## Concourse YAML — Agent Step Ergonomics {#yaml-ergonomics}

When writing agent steps in Concourse-compatible YAML pipelines, two defaults
reduce boilerplate.

### Auto-output volume

If you do not declare a `config.outputs` block, the runtime automatically
creates an output volume named after the agent. This volume holds the
`result.json` written at the end of the agent run and is registered in the job's
known mounts so subsequent steps can reference it.

```yaml
# Before — explicit output declaration required
- agent: code-reviewer
  prompt: Review the code
  model: openrouter/google/gemini-2.0-flash
  config:
    platform: linux
    image: alpine
    outputs:
      - name: code-reviewer # redundant — same as agent name

# After — outputs block can be omitted entirely
- agent: code-reviewer
  prompt: Review the code
  model: openrouter/google/gemini-2.0-flash
  config:
    platform: linux
    image: alpine
```

### Auto-inputs from `context.tasks`

If `context.tasks` references a prior agent by name and that agent produced an
auto-named output volume (see above), the volume is automatically mounted as an
input. There is no need to list it in `config.inputs`.

```yaml
# Before — explicit inputs + context.tasks both required
- agent: summarizer
  prompt: Summarize the findings
  model: openrouter/google/gemini-2.0-flash
  config:
    platform: linux
    image: alpine
    inputs:
      - name: code-reviewer # duplicate: also listed in context.tasks
  context:
    tasks:
      - name: code-reviewer

# After — config.inputs can be omitted
- agent: summarizer
  prompt: Summarize the findings
  model: openrouter/google/gemini-2.0-flash
  config:
    platform: linux
    image: alpine
  context:
    tasks:
      - name: code-reviewer
```

## Loading config from a URI {#uri}

Both task steps and agent steps accept a `uri` field as an alternative to
`file`. The `uri` field supports three schemes:

| Scheme     | Description                                           |
| ---------- | ----------------------------------------------------- |
| `file://`  | Load from a volume mount (same path format as `file`) |
| `http://`  | Fetch config over HTTP                                |
| `https://` | Fetch config over HTTPS                               |

`file` and `uri` are **mutually exclusive** — specifying both is a validation
error.

### `file://` URIs

A `file://` URI resolves against known volume mounts, using the same
`mountname/relative/path` format as the `file` field. Path traversal with `..`
is not allowed.

```yaml
# These two are equivalent:
- task: my-task
  file: repo/tasks/build.yml

- task: my-task
  uri: "file://repo/tasks/build.yml"
```

### `http://` and `https://` URIs

HTTP URIs fetch the YAML config from a remote server. The response must return a
2xx status code; non-OK responses are treated as errors.

```yaml
- task: my-task
  uri: "https://example.com/tasks/build.yml"

- agent: code-reviewer
  uri: "https://example.com/agents/reviewer.yml"
  model: openrouter/google/gemini-2.0-flash
```

The fetched content is parsed as YAML and merged with any inline fields on the
step (inline values override fetched values, prompts are concatenated).

## Tools {#tools}

The `tools` array lets you give an agent additional capabilities beyond the
built-in sandbox tools. Each entry is either an **agent tool** (LLM sub-agent)
or a **task tool** (container command), distinguished by field presence.

### Agent tools

An entry with an `agent:` field creates an LLM sub-agent that the parent can
call as a tool. The sub-agent gets its own prompt, model, and optionally its own
container image.

```yaml
- agent: orchestrator
  file: repo/agents/orchestrator.yml
  config:
    platform: linux
    image: alpine/git
    inputs:
      - name: repo
      - name: diff
  tools:
    - agent: code-quality-reviewer
      file: repo/agents/code-quality.yml
    - agent: security-reviewer
      file: repo/agents/security.yml
```

| Field    | Type   | Description                                                               |
| -------- | ------ | ------------------------------------------------------------------------- |
| `agent`  | string | **Required.** Tool name the parent LLM uses to call this agent            |
| `file`   | string | Load prompt, model, and config from a YAML file on a volume               |
| `uri`    | string | Load config from a URI (`file://`, `http://`, `https://`)                 |
| `prompt` | string | Agent instruction (concatenated with `file:`/`uri:` prompt if both exist) |
| `model`  | string | Model specifier; defaults to the parent's model if omitted                |

**Shared-container** (agent image matches the parent's or is omitted): the
sub-agent runs inside the parent's ADK session, sharing the same sandbox,
mounts, and tool set.

**Own-container** (agent declares a different `config.image`): a separate
sandbox is spun up. The agent runs to completion and returns its final text to
the parent. Results are persisted at a nested storage path:

```
jobs/{job}/N/agent/{parent}/sub-agents/{tool-name}/run
```

### Task tools

An entry with a `task:` field creates a container command the LLM can call as a
tool. The command runs in the parent's sandbox and returns stdout/stderr/exit
code.

```yaml
tools:
  - task: run-linter
    description: "Run the project linter and return results"
    config:
      run:
        path: golangci-lint
        args: ["run", "./..."]
      env:
        GOPROXY: "off"
  - task: post-comment
    description: "Post a GitHub PR comment"
    file: repo/tasks/post-comment.yml
```

| Field         | Type   | Description                                                    |
| ------------- | ------ | -------------------------------------------------------------- |
| `task`        | string | **Required.** Tool name the parent LLM uses to call this task  |
| `description` | string | Description shown to the LLM (defaults to "Run task: {name}")  |
| `config`      | object | Task config with `run.path`, `run.args`, `env`, `image`        |
| `file`        | string | Load task config from a YAML file on a volume                  |
| `uri`         | string | Load task config from a URI (`file://`, `http://`, `https://`) |

When the LLM calls a task tool, it can pass a `request` string that is set as
the `TOOL_REQUEST` environment variable, allowing dynamic input.

### YAML example — pr-review pipeline

Below is a simplified version of the multi-reviewer PR analysis pipeline from
[`examples/agent/pr-review.yml`](../../examples/agent/pr-review.yml):

```yaml
- agent: final-review
  file: repo/examples/agent/agents/final-reviewer.yml
  tools:
    - agent: code-quality-reviewer
      file: repo/examples/agent/agents/specialist-reviewer.yml
      prompt: "Review for code quality — readability, naming, structure, DRY violations."
    - agent: security-reviewer
      file: repo/examples/agent/agents/specialist-reviewer.yml
      prompt: "Audit for security issues — injection, authentication, data exposure, OWASP Top 10."
    - agent: maintainability-reviewer
      file: repo/examples/agent/agents/specialist-reviewer.yml
      prompt: "Evaluate for maintainability — test coverage, cyclomatic complexity, documentation."
  validation:
    expr: 'text != "" && text contains "summary"'
    prompt: >-
      You must output valid JSON containing a "summary" field and an
      "issues" array. Do not include prose outside the JSON object.
```

All three specialist tools share a single `specialist-reviewer.yml` template,
differentiated by the inline `prompt` field. The orchestrator's prompt (in
`final-reviewer.yml`) instructs it to call all three and synthesize their
findings into JSON.

## Return Value

### Audit Log {#audit-log}

```typescript
{
  text: string; // final agent response text
  status: string; // "success"
  toolCalls: Array<{
    name: string;
    args?: Record<string, unknown>;
    result?: Record<string, unknown>;
    exitCode?: number;
  }>;
  usage: {
    promptTokens: number;
    completionTokens: number;
    totalTokens: number;
    llmRequests: number;
    toolCallCount: number;
  }
  auditLog: Array<AuditEvent>; // full ordered conversation log (see below)
}
```

The `auditLog` field contains every event that occurred during the agent run in
chronological order. It is stored in the pipeline run's storage payload
alongside `stdout`, `toolCalls`, and `usage` for offline inspection.

```typescript
interface AuditEvent {
  type:
    | "pre_context"
    | "user_message"
    | "tool_call"
    | "tool_response"
    | "model_text"
    | "model_final";
  timestamp?: string; // ISO 8601 UTC
  invocationId?: string; // groups events within one LLM turn
  author?: string; // agent name or "user"
  text?: string; // model text or user prompt
  toolName?: string; // for tool_call / tool_response / pre_context
  toolCallId?: string; // pairs a tool_call with its tool_response
  toolArgs?: Record<string, unknown>;
  toolResult?: Record<string, unknown>;
  usage?: { // per-event token counts (model events only)
    promptTokens: number;
    completionTokens: number;
    totalTokens: number;
  };
}
```

| `type`          | When emitted                                                                             |
| --------------- | ---------------------------------------------------------------------------------------- |
| `pre_context`   | Synthetic tool result injected before the first turn (list_tasks or context.tasks entry) |
| `user_message`  | The initial prompt sent by the pipeline                                                  |
| `tool_call`     | The model requests a tool invocation                                                     |
| `tool_response` | The tool result is returned to the model                                                 |
| `model_text`    | An intermediate text chunk from the model                                                |
| `model_final`   | The concluding model response (last turn)                                                |

## Callbacks {#callbacks}

Agent runs support optional callbacks for streaming output, incremental usage
updates, and audit event notifications. All callbacks are optional.

```typescript
await agent.run({
  name: "agent",
  prompt: "Build the project",
  model: "anthropic/claude-3-5-sonnet-20241022",
  image: "golang:1.22",
  onOutput: (stream, chunk) => {
    // stream is "stdout" or "stderr"
    console.log(`${stream}: ${chunk}`);
  },
  onUsage: (usage) => {
    // called after every LLM turn or tool invocation that changes token counts
    console.log(`Tokens: ${usage.totalTokens}, Requests: ${usage.llmRequests}`);
  },
  onAuditEvent: (event) => {
    // called once per audit event (model_text, tool_call, etc.)
    console.log(`Event: ${event.type}`);
  },
});
```

| Callback       | Type                                                    | When invoked                                      |
| -------------- | ------------------------------------------------------- | ------------------------------------------------- |
| `onOutput`     | `(stream: "stdout" \| "stderr", chunk: string) => void` | Each stdout/stderr chunk from sandbox tool calls  |
| `onUsage`      | `(usage: UsageMetrics) => void`                         | After every token count or tool call count change |
| `onAuditEvent` | `(event: AuditEvent) => void`                           | After every audit event (model turn, tool result) |

**Type definitions:**

```typescript
interface UsageMetrics {
  promptTokens: number;
  completionTokens: number;
  totalTokens: number;
  llmRequests: number;
  toolCallCount: number;
}
```

The `onUsage` and `onAuditEvent` callbacks power the
[progressive persistence](#progressive-persistence) feature — they are
automatically wired to incremental storage writes when called from the pipeline
runner. Custom callbacks can observe the same events in real time.

## Examples

### Minimal agent

```typescript
const result = await agent.run({
  name: "summarize",
  prompt: "Summarize the files in /workspace",
  model: "openrouter/google/gemini-2.0-flash",
  image: "alpine",
});

console.log(result.text);
```

### Agent with volumes and LLM tuning

```typescript
const repo = await volumes.create("repo", 500);

await runtime.run({
  name: "clone",
  image: "alpine/git",
  command: {
    path: "git",
    args: ["clone", "https://github.com/example/app", "/repo"],
  },
  mounts: { "/repo": repo },
});

const result = await agent.run({
  name: "review",
  prompt: "Review the code for security issues and summarize findings.",
  model: "anthropic/claude-3-5-sonnet-20241022",
  image: "alpine",
  mounts: { "repo": repo },
  llm: { temperature: 0.1, max_tokens: 4096 },
  thinking: { budget: 2048 },
  safety: { dangerous_content: "block_only_high" },
  contextGuard: { strategy: "threshold", max_tokens: 80000 },
});
```

### Streaming output callback

```typescript
await agent.run({
  name: "agent",
  prompt: "Run the test suite and report failures.",
  model: "openrouter/anthropic/claude-3-5-sonnet",
  image: "golang:1.22",
  mounts: { "src": srcVolume },
  onOutput: (stream, chunk) => {
    // stream is "stdout" or "stderr"
    process.stdout.write(chunk);
  },
});
```
