# Pipeline Runtime API

The runtime is the JavaScript/TypeScript execution environment for pipelines. It
provides globals and functions for running containers, managing volumes, sending
HTTP requests, and accessing pipeline context.

## Global API

All pipelines have access to these globals:

- `runtime` — container execution and volume management
- `http` — webhook request/response handling
- `pipelineContext` — metadata about the current execution
- `assert` — test assertions (useful for inline validation)

## Example

```typescript
const pipeline = async () => {
  console.log("Starting pipeline:", pipelineContext.name);

  const vol = await volumes.create("data", 100);
  const result = await runtime.run({
    name: "build",
    image: "golang:1.22",
    command: { path: "go", args: ["build", "./..."] },
    mounts: { "/workspace": vol },
  });

  console.log("Exit code:", result.code);
  assert.equal(result.code, 0, "build must succeed");
};

export { pipeline };
```

Browse detailed docs on the left.
