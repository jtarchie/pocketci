# PocketCI

PocketCI is a local-first CI/CD runtime. You write pipelines in
JavaScript/TypeScript, and it runs them in containers on your machine -- no
hosted service, no YAML gymnastics, no waiting.

<!-- deno-fmt-ignore-start -->
> [!NOTE]
> PocketCI is under active development. Things may change.
<!-- deno-fmt-ignore-end -->

## What does a pipeline look like?

A pipeline is just an exported async function. You call `runtime.run()` to spin
up containers, pass in commands, and get results back. Here's the gist:

```ts
const pipeline = async () => {
  const result = await runtime.run({
    name: "hello",
    image: "alpine",
    command: { path: "echo", args: ["Hello from PocketCI!"] },
  });
  assert.containsString(result.stdout, "Hello from PocketCI!");
};

export { pipeline };
```

That's a real, runnable pipeline. No build configs, no glue code. You have the
full expressiveness of TypeScript -- loops, conditionals, error handling, shared
volumes -- and it all runs inside containers managed by PocketCI.

If you're coming from Concourse CI, PocketCI also supports YAML pipelines with
backward compatibility. See the [YAML Pipelines guide](docs/guides/yaml-pipelines.md) for details.

## Running it

With Docker running, a single command executes a pipeline:

```bash
pocketci runner examples/both/hello-world.ts
```

Or start the server for a web UI and API:

```bash
pocketci server
```

The [CLI reference](docs/cli/) covers all available commands and options.

## Why PocketCI?

Most CI systems are either cloud-hosted black boxes or complex self-hosted
installations. PocketCI takes a different approach: your pipelines run locally,
the runtime is transparent and programmable, and you can swap orchestration
backends (Docker, Kubernetes, Fly.io, and others) without rewriting your
pipelines. It stores everything in a single SQLite database.

## Documentation

The full docs live under [`docs/`](docs/) and are served at `/docs/` when
running the PocketCI server. Topics include the [runtime API](docs/runtime/),
[secrets](docs/operations/secrets), [caching](docs/operations/caching),
[driver configuration](docs/drivers/index), and
[webhook integrations](docs/guides/webhooks).

## Contributing

```bash
brew bundle        # install toolchain
task               # build, lint, test -- everything
```

Take a look at [`examples/`](examples/) for real pipelines that run as part of
the test suite. The project uses Go, TypeScript, and
[go-task](https://taskfile.dev) as its build runner.
