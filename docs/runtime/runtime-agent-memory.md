# Agent Memory

Agents are stateless by default: each run rediscovers the same facts and
re-derives the same fixes. Enabling **memory** lets an agent save short, durable
lessons in one run and recall them in later runs of the same pipeline.

Memory is opt-in per agent, scoped by `(pipeline, agent-name)`, stored in
SQLite, and searched with FTS5.

## Enabling

```typescript
await agent.run({
  name: "reviewer",
  model: "anthropic/claude-opus-4-7",
  image: "alpine",
  memory: { enabled: true },
  prompt: `Before you start, call recall_memory to reuse prior lessons. When
           you discover a convention or known fix, call save_memory with a
           short description and tags.`,
});
```

YAML (Concourse-style):

```yaml
- agent: reviewer
  prompt: ...
  model: anthropic/claude-opus-4-7
  memory:
    enabled: true
    max_recall: 10
  config:
    platform: linux
    image: alpine
```

## Config

| Field               | Type    | Default | Description                                    |
| ------------------- | ------- | ------- | ---------------------------------------------- |
| `enabled`           | boolean | `false` | Required. Memory is strictly opt-in.           |
| `max_recall`        | integer | `5`     | Maximum memories returned per `recall_memory`. |
| `max_content_bytes` | integer | `4096`  | Maximum bytes per saved memory.                |

## Tools

With memory enabled, the agent gains two tools:

### `recall_memory`

Search prior memories ranked by relevance.

| Input   | Type     | Description                                       |
| ------- | -------- | ------------------------------------------------- |
| `query` | string   | Free-text query. Empty returns most recent.       |
| `tags`  | string[] | Narrow to memories matching all given tags.       |
| `limit` | integer  | Override `max_recall` downward. Capped by config. |

Returns ranked results with `content`, `tags`, `created_at`, `recall_count`, and
`last_recalled`. Hits bump `recall_count` and `last_recalled`.

### `save_memory`

Persist a short lesson. Identical content is deduplicated — saving the same
string twice is a no-op.

| Input     | Type     | Description                                        |
| --------- | -------- | -------------------------------------------------- |
| `content` | string   | Required. Short lesson, under `max_content_bytes`. |
| `tags`    | string[] | Optional categorical tags for filtering.           |

Returns `{ saved: true, deduped: boolean }`.

## Scoping

Memories are strictly isolated:

- Between pipelines (agent in pipeline A cannot read pipeline B).
- Between agent names in the same pipeline (agent `reviewer` ≠ agent `builder`).

When a pipeline is deleted, its memories are deleted via `ON DELETE CASCADE`.

## What to save

**Good:** conventions specific to this pipeline, known fixes for recurring
failures, canonical file paths the agent had to discover, compatible dependency
versions, lessons that apply across PRs.

**Avoid:** run-specific state (use task outputs instead), secrets (use the
secrets manager), large blobs (cap is 4KB by design).
