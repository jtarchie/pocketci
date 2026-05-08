# image.build()

Builds a container image from a Dockerfile and (optionally) pushes it to a
registry. The build runs as an ordinary task using
[`moby/buildkit`](https://github.com/moby/buildkit) with
`buildctl-daemonless.sh`, which is the same builder that backs `docker buildx`.
Subsequent tasks reference the image by its tag — every orchestra driver pulls
images by name, so no special handoff is required.

## TypeScript

```typescript
const result = await image.build({
  context: "source", // input volume mount name
  dockerfile: "Dockerfile", // path relative to context (default: "Dockerfile")
  tag: "registry.example.com/myapp:latest",
  push: true,
  buildArgs: { GO_VERSION: "1.26" },
  target: "production", // optional Dockerfile stage
  platforms: ["linux/amd64"], // optional; default host arch
  inputs: { source: sourceVolume }, // VolumeResult mounts
  caches: { "/var/lib/buildkit": cacheVolume }, // optional layer cache mount
  registryAuth: {
    registry: "registry.example.com", // optional; inferred from tag
    username: "user",
    password: "secret:REGISTRY_PASSWORD", // resolved by the secrets manager
    insecure: false, // permit plain-HTTP push
  },
  timeout: "10m",
  onOutput: (stream, data) => process.stdout.write(data),
});

console.log(result.ref); // "registry.example.com/myapp:latest"
console.log(result.digest); // "sha256:..."

// Use the built image in the next task:
await runtime.run({
  image: result.ref,
  command: { path: "/myapp", args: ["--version"] },
});
```

## YAML / Concourse-compat

```yaml
plan:
  - task: checkout
    config:
      platform: linux
      image_resource: { type: registry-image, source: { repository: alpine } }
      outputs: [{ name: source }]
      run: { path: sh, args: ["-c", "git clone $REPO source"] }

  - build_image:
      name: build-app
      context: source
      dockerfile: source/Dockerfile
      tag: registry.example.com/myapp:latest
      push: true
      build_args:
        GO_VERSION: "1.26"
      target: production
      platforms: [linux/amd64]
      inputs:
        - name: source
      caches:
        - path: /var/lib/buildkit
      registry:
        hostname: registry.example.com
        username: secret:REGISTRY_USER
        password: secret:REGISTRY_PASSWORD
        insecure: false

  - task: smoke-test
    image: registry.example.com/myapp:latest
    config:
      platform: linux
      run: { path: /myapp, args: [--version] }
```

The `build_image` step reuses the same input/output volume machinery as `task`
steps, so its `context` typically refers to an output produced by an earlier
task.

## Privilege requirement

The build container runs with `privileged: true`. BuildKit needs that to set up
its OCI worker on Linux kernels. A future release will add a rootless opt-in
once the orchestra layer exposes seccomp/security options uniformly across
drivers.

## Layer caching

Mount a cache volume at `/var/lib/buildkit` to persist BuildKit's local layer
cache across runs. The volume cooperates with the existing
[caches](../operations/caching.md) system — the cache layer transparently
restores the volume contents on volume creation and persists them at job end.

```yaml
- build_image:
    context: source
    tag: registry.example.com/myapp:latest
    push: true
    caches:
      - path: /var/lib/buildkit
```

## Registry authentication

Credentials are written into `/root/.docker/config.json` inside the build
container. Username and password may be `secret:KEY` references — the runner
resolves them through the secrets manager and tracks the resolved values for log
redaction.

For pushing to a local test registry that serves plain HTTP, set
`registry.insecure: true`. Never enable this for production registries.

## Result

`image.build()` resolves to an object with:

- `ref` — the tag you supplied, echoed for convenience
- `digest` — the OCI image digest (`sha256:...`) extracted from BuildKit's
  metadata file
- `code` / `stdout` / `stderr` — the underlying container run result, useful for
  debugging build failures

A non-zero exit code rejects the promise with the build container's logs.
