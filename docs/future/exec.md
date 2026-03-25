Research container exec patterns

Read [](file:///Users/jtarchie/workspace/ci/orchestra/orchestrator.go)

Read [](file:///Users/jtarchie/workspace/ci/orchestra/task.go)

Read [](file:///Users/jtarchie/workspace/ci/runtime/pipeline_runner.go#1-1),
lines 1 to 150

Read [](file:///Users/jtarchie/workspace/ci/runtime/runner_interface.go)

Searched for files matching `**/orchestra/docker/container.go`, 1 match

Searched for files matching `**/orchestra/native/container.go`, 1 match

Searched for files matching `**/orchestra/fly/container.go`, 1 match

Searched for files matching `**/orchestra/vz/container.go`, 1 match

Searched for files matching `**/orchestra/qemu/container.go`, 1 match

Searched for files matching `**/orchestra/k8s/container.go`, 1 match

Read [](file:///Users/jtarchie/workspace/ci/orchestra/docker/container.go#1-1),
lines 1 to 200

Read [](file:///Users/jtarchie/workspace/ci/orchestra/docker/docker.go)

Read [](file:///Users/jtarchie/workspace/ci/orchestra/native/container.go)

Read [](file:///Users/jtarchie/workspace/ci/orchestra/native/native.go)

Read [](file:///Users/jtarchie/workspace/ci/orchestra/k8s/container.go#1-1),
lines 1 to 200

Read [](file:///Users/jtarchie/workspace/ci/orchestra/fly/container.go#1-1),
lines 1 to 200

Read [](file:///Users/jtarchie/workspace/ci/orchestra/vz/container.go#1-1),
lines 1 to 150

Read [](file:///Users/jtarchie/workspace/ci/orchestra/qemu/container.go#1-1),
lines 1 to 150

Read [](file:///Users/jtarchie/workspace/ci/runtime/pipeline_runner.go#150-150),
lines 150 to 450

Read [](file:///Users/jtarchie/workspace/ci/orchestra/k8s/container.go#200-200),
lines 200 to 621

Read
[](file:///Users/jtarchie/workspace/ci/orchestra/docker/container.go#200-200),
lines 200 to 244

Good research. Let me clarify a few design decisions before drafting the plan.

Asked 4 questions (API shape, Idle process, Driver scope, JS/TS API)

Now I have enough to draft the plan. Let me also check the existing runtime JS
API to understand current `runtime.run` shape before finalizing.

Research runtime JS/TS API surface

Read [](file:///Users/jtarchie/workspace/ci/runtime/runtime.go)

Read [](file:///Users/jtarchie/workspace/ci/runtime/pipeline_runner.go#1-1),
lines 1 to 300

Read [](file:///Users/jtarchie/workspace/ci/runtime/runner_interface.go)

Read [](file:///Users/jtarchie/workspace/ci/orchestra/orchestrator.go)

Read [](file:///Users/jtarchie/workspace/ci/orchestra/docker)

Read [](file:///Users/jtarchie/workspace/ci/orchestra/native)

Read [](file:///Users/jtarchie/workspace/ci/packages/ci)

Read [](file:///Users/jtarchie/workspace/ci/orchestra/docker/container.go)

Read [](file:///Users/jtarchie/workspace/ci/orchestra/native/container.go)

Read [](file:///Users/jtarchie/workspace/ci/runtime/pipeline_runner.go#300-300),
lines 300 to 615

Read [](file:///Users/jtarchie/workspace/ci/packages/ci/src)

Read [](file:///Users/jtarchie/workspace/ci/packages/ci/src/global.d.ts#1-1),
lines 1 to 300

Read [](file:///Users/jtarchie/workspace/ci/orchestra/orchestrator.go)

Read [](file:///Users/jtarchie/workspace/ci/orchestra/task.go)

Now I have everything I need. Here is the plan:

---

## Plan: Multi-command Sandbox Execution

Support running multiple commands sequentially inside a single long-lived
container, with shell state (cwd, exports) persisting between `exec` calls. A
new optional `SandboxDriver` interface keeps the existing `Driver`/`Container`
interfaces untouched; drivers opt in.

**How the shell-stdin approach works**: The sandbox starts the container running
`sh -i`. Each `Exec` call writes `(the command); printf "SANDBOX_EXIT_$?\n"` to
the shell's piped stdin and reads stdout/stderr until the sentinel appears —
giving us exit code and output per command, while the shell session persists.

---

**Steps**

1. **Add `SandboxDriver` and `Sandbox` interfaces** to orchestrator.go:
   - `SandboxDriver` with `StartSandbox(ctx, task Task) (Sandbox, error)` —
     drivers type-assert to this when callers need sandbox support
   - `Sandbox` with
     `Exec(ctx, cmd []string, env map[string]string, stdin io.Reader, stdout, stderr io.Writer) (ContainerStatus, error)`,
     `Cleanup(ctx) error`, and `ID() string`

2. **Docker sandbox** — new file `orchestra/docker/sandbox.go`:
   - `StartSandbox`: pulls image, creates container with
     `Cmd: []string{"sh", "-i"}`, `OpenStdin: true`, `AttachStdin: true`; calls
     `ContainerAttach` to get a hijacked conn giving `stdin`/`stdout` pipes;
     starts the container; returns a `dockerSandbox` holding the conn
   - `dockerSandbox.Exec`: generates a UUID sentinel, writes
     `(cmd args...); printf "SANDBOX_EXIT_$?\n"` to the attached stdin, reads
     stdout/stderr line-by-line until sentinel, parses exit code, returns result
   - `Cleanup`: writes `exit\n` then `ContainerRemove(Force: true)`

3. **Native sandbox** — new file `orchestra/native/sandbox.go`:
   - `StartSandbox`: creates a temp working dir, launches
     `exec.Command("sh", "-i")` with piped stdin/stdout/stderr; returns a
     `nativeSandbox`
   - `nativeSandbox.Exec`: same sentinel protocol — write command + sentinel to
     stdin pipe, drain output until sentinel appears
   - Env vars passed per-exec by writing `export KEY=VALUE` lines before the
     command

4. **k8s sandbox** — new file `orchestra/k8s/sandbox.go`:
   - `StartSandbox`: creates a `batch/v1 Job` with `sh -i` and `stdin: true` in
     the pod spec; waits for pod `Running`; uses the already-imported
     `remotecommand.NewSPDYExecutor` with the pod `attach` subresource to open a
     stream
   - `Exec`: same sentinel write/read protocol over the SPDY stream

5. **Fly/QEMU/VZ stubs** — each gets a `sandbox.go` that returns
   `ErrNotSupported` (or similar) from `StartSandbox`. This satisfies the
   architecture without blocking on their more complex agent work. Fly machines
   can be revisited using `fly machine exec`.

6. **Runtime layer — `SandboxHandle`** — new file `runtime/sandbox_runner.go`:
   - `SandboxInput` struct mirrors `RunInput` minus `Command` (image, env,
     mounts, work_dir, name, etc.)
   - `SandboxHandle` wraps a live `orchestra.Sandbox`; its `Exec(ExecInput)`
     method maps to `sandbox.Exec(...)` with the same output streaming, secret
     redaction, and timeout logic as `PipelineRunner.Run`
   - `SandboxHandle.Close()` calls `sandbox.Cleanup(ctx)` and removes any
     storage entries

7. **Extend the `Runner` interface** in runner_interface.go:
   - Add `StartSandbox(input SandboxInput) (*SandboxHandle, error)`
   - Update `PipelineRunner` and `ResumableRunner` (the two concrete `Runner`
     types) to implement it
   - In `PipelineRunner`: type-assert `c.client` to `orchestra.SandboxDriver`;
     return a descriptive error if the driver doesn't support it

8. **Goja registration** in runtime.go:
   - Register `runtime.startSandbox` analogously to `runtime.run` — same
     promise/goroutine pattern; resolves to a JS object with `exec(config)`
     (promise) and `close()` (promise) methods exposed via `r.jsVM.ToValue`

9. **TypeScript types** in global.d.ts:
   - Add `runtime.startSandbox(config: SandboxConfig): Promise<SandboxHandle>`
   - `SandboxConfig`: `image`, `env?`, `mounts?`, `workDir?`, `privileged?`,
     `name`
   - `SandboxHandle`: `exec(config: ExecConfig): Promise<RunTaskResult>`,
     `close(): Promise<void>`, `id: string`
   - `ExecConfig`: `command`, `env?`, `stdin?`, `timeout?`, `onOutput?`
   - Run `go generate workspace.` to rebuild the bundle after any changes to ci

10. **Tests**:
    - Unit tests per driver in `orchestra/docker/sandbox_test.go`,
      `orchestra/native/sandbox_test.go`, `orchestra/k8s/sandbox_test.go` —
      start sandbox, exec multiple commands, assert state persists (e.g.,
      `export FOO=bar` then `echo $FOO`)
    - Integration test in examples (both drivers) showing a real multi-step
      sandbox use case
    - `runtime/sandbox_runner_test.go` validating secret redaction and timeout
      behaviour

---

**Verification**

```bash
go test -race ./orchestra/... -count=1 -parallel=1   # driver-level sandbox tests
go test -race ./runtime/... -count=1 -parallel=1     # runtime sandbox tests
go test -race ./examples/... -count=1 -parallel=1    # end-to-end with docker + native
task default                                          # full lint + typecheck + build
```

---

**Decisions**

- **Separate `SandboxDriver` interface over modifying `Container`**: keeps
  backward compatibility; drivers that can't support exec (e.g., Fly initially)
  don't need to; callers get a clear `unsupported` error rather than a runtime
  panic
- **Shell-with-sentinel over `ContainerExecCreate`**: allows shell state (cwd,
  env exports) to persist across `Exec` calls, which is important for agent
  sandbox use cases; UUID sentinels (`SANDBOX_EXIT_<uuid>_$?`) prevent
  accidental collision with command output
- **Explicit `close()` over callback scope**: gives JS callers flexibility to
  hold the sandbox open across async gaps without nesting depth
