# YAML Templating

PocketCI supports optional Go text/template rendering for YAML pipeline files
using the Sprig function library. This allows you to reduce duplication and
parameterize your pipelines using template variables and helper functions.

## Opt-In Marker

Templating is **opt-in**. To enable it, add the marker `# pocketci: template`
anywhere on the first line of your YAML file:

```yaml
# pocketci: template
---
jobs: []
```

Or at the end of a comment:

```yaml
# My pipeline configuration - pocketci: template
---
jobs: []
```

If the marker is not present, the YAML is processed unchanged (backwards
compatible).

## How It Works

1. PocketCI reads your YAML pipeline file
2. If the first line contains `pocketci: template`, the entire file is parsed as
   a Go text/template
3. The template is rendered using the [Sprig] function library (and no runtime
   context)
4. The rendered output is validated and executed as normal YAML

This preprocessing happens at **pipeline validation time** (when you run
`pocketci pipeline set`) and at **execution time** (when you run
`pocketci pipeline run`), ensuring the latest template rendering is always used.

## Available Functions

Most [Sprig functions] are available, including:

- **Case conversion**: `upper`, `lower`, `title`, `snakecase`, `camelcase`
- **String operations**: `replace`, `repeat`, `substr`, `join`, `split`, `trim`,
  `contains`
- **List operations**: `first`, `last`, `reverse`, `sort`, `unique`, `concat`
- **Math**: `add`, `sub`, `mul`, `div`, `min`, `max`
- **Defaults**: `default`, `coalesce`, `empty`
- **Type functions**: `type`, `string`, `int`, `float`

> **Security note:** The `env` and `expandenv` functions are intentionally
> unavailable. Templates cannot read server environment variables (which may
> include secrets). Use PocketCI's [Secrets](../operations/secrets.md) feature
> instead.

See the [Sprig documentation] for the complete list of available functions.

## Example: Reducing Duplicate Agent Prompts

If you have multiple agent steps with nearly identical instructions, you can use
templating to reduce duplication:

```yaml
# pocketci: template
---
jobs:
  - name: review-pr
    plan:
      - agent: code-quality-reviewer
        prompt: |
          Review the diff for code quality issues.
          {{ $commonInstructions }}

      - agent: security-reviewer
        prompt: |
          Review the diff for security issues.
          {{ $commonInstructions }}
```

Where `$commonInstructions` is a template variable defined earlier in the file.

## Example: Parameterizing Image Versions

Use Sprig functions to generate platform-specific image paths:

```yaml
# pocketci: template
---
vars:
  image_repo: "my-registry.example.com"

jobs:
  - name: build
    plan:
      - task: docker-build
        config:
          image_resource:
            type: registry-image
            source:
              repository: {{ .image_repo }}/{{ lower "BuildTools" }}
              tag: v1.0
```

## Example: Looping Over Values

Generate multiple jobs from a template:

```yaml
# pocketci: template
---
{{ $platforms := list "linux" "darwin" "windows" }}
jobs:
{{ range $platforms }}
  - name: build-{{ . }}
    plan:
      - task: compile
        config:
          env:
            TARGET_OS: {{ upper . }}
{{ end }}
```

## Error Handling

- **Parse errors** (e.g., unclosed `{{ }}` tags): Fails with
  `pipeline template parse failed` during validation
- **Undefined functions** (e.g., `{{ unknownFunc }}`): Fails with
  `pipeline template parse failed` (Go validates at parse time)
- **Rendering errors**: Fails with `pipeline template render failed` during
  validation

All errors occur at validation time, before the pipeline is uploaded, providing
immediate feedback.

## Important Notes

1. **No runtime context**: Templates are rendered at validation/execution time
   with no access to runtime variables, secrets, or task outputs. Use PocketCI's
   job triggers, parameters, and secrets features for dynamic behavior.

2. **Backwards compatible**: Pipelines without the `# pocketci: template` marker
   are processed unchanged.

3. **First-line only**: The marker must appear on the first line (after any
   shebang). Markers on subsequent lines are ignored.

4. **Strict mode**: Templating uses Go's default strict mode, failing if a
   function is undefined or a required field is missing. There is no silent
   fallback.

## See Also

- [Sprig documentation][sprig documentation]
- [Go text/template reference](https://pkg.go.dev/text/template)
- [Sprig documentation][sprig documentation]

[sprig]: https://github.com/go-task/slim-sprig
[sprig documentation]: https://masterminds.github.io/sprig/
