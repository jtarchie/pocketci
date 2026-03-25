# PocketCI Documentation

PocketCI is a local-first pipeline runtime that executes JavaScript/TypeScript
pipelines and supports Concourse-style YAML compatibility.

## Getting Started

- [Running pipelines](guides/run)
- [Webhooks](guides/webhooks)
- [MCP Integration](guides/mcp)

## Operations

- [Authentication](operations/authentication)
- [Authorization (RBAC)](operations/rbac)
- [Secrets Management](operations/secrets)
- [Caching](operations/caching)
- [Feature Gates](operations/feature-gates)

## Drivers

- [Native Resources](drivers/native-resources)
- [Implementing a New Driver](drivers/implementing-driver)

## API Reference

- [API Overview](api/)
- [Pipelines API](api/pipelines)
- [Runs API](api/runs)
- [Webhooks API](api/webhooks)
- [MCP](api/mcp)

## CLI Commands

- [CLI Overview](cli/)
- [Runner](cli/runner)
- [Server](cli/server)
- [Login](cli/login)
- [Pipeline Set](cli/pipeline-set)
- [Pipeline Run](cli/pipeline-run)
- [Pipeline Rm](cli/pipeline-rm)
- [Pipeline Ls](cli/pipeline-ls)
- [Pipeline Pause](cli/pipeline-pause)
- [Pipeline Unpause](cli/pipeline-unpause)

## Runtime

- [Runtime API](runtime/)
- [runtime.run()](runtime/runtime-run)
- [Volumes](runtime/volumes)

## Notes

Draft and exploratory material is under `docs/future/` and is not considered
stable user-facing documentation.
