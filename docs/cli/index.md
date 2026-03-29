# CLI Reference

The `ci` command-line tool provides these commands:

- **`pocketci runner`**: Execute pipelines locally in Docker, native, or other
  drivers
- **`pocketci server`**: Start an HTTP server that manages pipelines and
  executes them on demand
- **`pocketci login`**: Authenticate with a remote server via OAuth device flow
- **`pocketci pipeline set`**: Store pipelines on a remote server (requires a
  running `pocketci server`)
- **`pocketci pipeline run`**: Execute a stored pipeline on a remote server
- **`pocketci pipeline trigger`**: Trigger async pipeline execution
- **`pocketci pipeline rm`**: Remove a pipeline from a remote server
- **`pocketci pipeline ls`**: List all pipelines on a remote server
- **`pocketci pipeline pause`**: Pause a pipeline to prevent new runs
- **`pocketci pipeline unpause`**: Unpause a pipeline to allow new runs
- **`pocketci pipeline schedule ls`**: List schedules for a pipeline
- **`pocketci pipeline schedule pause`**: Pause a schedule
- **`pocketci pipeline schedule unpause`**: Unpause a schedule

Browse commands below, or use `pocketci <command> --help` for quick reference.
