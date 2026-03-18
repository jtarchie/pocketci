# runtime.agent()

Run an LLM agent that can use a sandboxed container as a tool.

The agent receives a prompt, calls an LLM, and iteratively invokes built-in
tools backed by the container image you provide. Results stream back in real
time.

```typescript
const result = await runtime.agent(options);
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

| Field              | Type   | Description                                                          |
| ------------------ | ------ | -------------------------------------------------------------------- |
| `mounts`           | object | Volume mounts: `{ "name": volumeHandle }`                            |
| `outputVolumePath` | string | Path inside the container to write `result.json`                     |
| `llm`              | object | LLM generation overrides (see [LLM Config](#llm))                    |
| `thinking`         | object | Extended thinking config (see [Thinking](#thinking))                 |
| `safety`           | object | Safety filter overrides (see [Safety](#safety))                      |
| `context_guard`    | object | Context window management (see [Context Guard](#context-guard))      |
| `context`          | object | Pre-inject prior task outputs into session (see [Context](#context)) |

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
context_guard?: {
  strategy: "threshold" | "sliding_window";
  max_tokens?: number;  // threshold: evict history when total exceeds this
  max_turns?: number;   // sliding_window: keep only the last N turns
};
```

| Strategy         | `max_tokens` default | `max_turns` default | Behaviour                                            |
| ---------------- | -------------------- | ------------------- | ---------------------------------------------------- |
| `threshold`      | 128000               | _N/A_               | Truncates history once total tokens exceed the limit |
| `sliding_window` | _N/A_                | 30                  | Keeps only the most-recent N conversation turns      |

**Strategy inference:** If `context_guard` is provided with only `max_turns` (no
`strategy`), the strategy automatically becomes `"sliding_window"`. If only
`max_tokens` is provided, the strategy becomes `"threshold"`. An error is
returned if an invalid `strategy` is explicitly specified.

Omitting `context_guard` entirely disables context management; the full
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
`runtime.agent()` still includes the complete `auditLog` array in memory.

## Built-in Tools {#built-in-tools}

Every agent run has three tools available automatically — no configuration
required.

| Tool              | Description                                                             |
| ----------------- | ----------------------------------------------------------------------- |
| `run_script`      | Run a multi-line shell script inside the sandbox container              |
| `read_file`       | Read a file from a mounted volume without a shell subprocess            |
| `list_tasks`      | List all tasks in the current pipeline run with their status and timing |
| `get_task_result` | Fetch the stdout, stderr, and exit code for a specific task by name     |

`list_tasks` is **always pre-fetched** and injected into the session before the
agent's first turn, so the agent knows the run state immediately without
spending a tool-call round-trip on orientation.

`get_task_result` supports fuzzy name matching — a partial or approximate task
name is fine. Byte-length truncation is applied when the output is large
(default 4 096 bytes; override with `max_bytes` in the tool call or via
[`context.max_bytes`](#context)).

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
await runtime.agent({
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
const result = await runtime.agent({
  name: "summarize",
  prompt: "Summarize the files in /workspace",
  model: "openrouter/google/gemini-2.0-flash",
  image: "alpine",
});

console.log(result.text);
```

### Agent with volumes and LLM tuning

```typescript
const repo = await runtime.createVolume("repo", 500);

await runtime.run({
  name: "clone",
  image: "alpine/git",
  command: {
    path: "git",
    args: ["clone", "https://github.com/example/app", "/repo"],
  },
  mounts: { "/repo": repo },
});

const result = await runtime.agent({
  name: "review",
  prompt: "Review the code for security issues and summarize findings.",
  model: "anthropic/claude-3-5-sonnet-20241022",
  image: "alpine",
  mounts: { "repo": repo },
  llm: { temperature: 0.1, max_tokens: 4096 },
  thinking: { budget: 2048 },
  safety: { dangerous_content: "block_only_high" },
  context_guard: { strategy: "threshold", max_tokens: 80000 },
});
```

### Streaming output callback

```typescript
await runtime.agent({
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
