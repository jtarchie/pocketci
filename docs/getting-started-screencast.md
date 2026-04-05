# Getting Started — Screen Capture Script

This script drives the screen recording for the PocketCI Getting Started demo.
Follow each scene in order. `<!-- PAUSE -->` marks are natural cut/transition
points — stop typing, let the output settle, and give viewers time to read.

**Recording setup:**
- Terminal: 140 columns × 40 rows, large font (18pt+)
- Shell prompt: short (e.g. `$`)
- Browser: open to a blank tab before starting
- `hello.ts` pre-written and saved (contents shown in Scene 4)

---

## Scene 1 — Verify the install

```bash
pocketci --version
```

**Expected output:**
```
pocketci version v0.1.3 (abc1234)
```

<!-- PAUSE -->

---

## Scene 2 — Start the server

```bash
pocketci server --port 8080 --storage-sqlite-path pocketci.db
```

**Expected output (within a second or two):**
```
{"time":"...","level":"INFO","msg":"server.start","addr":":8080"}
```

<!-- PAUSE — switch to browser -->

---

## Scene 3 — Show the empty web UI

Switch to the browser. Navigate to:

```
http://localhost:8080/pipelines/
```

The pipeline list is empty. Narrate: *"This is the PocketCI dashboard. Right now
there are no pipelines — let's register one."*

<!-- PAUSE — switch back to terminal, open a second tab -->

---

## Scene 4 — Show the pipeline file

In a new terminal tab, display the pipeline source:

```bash
cat hello.ts
```

**Expected output:**
```typescript
const pipeline = async () => {
  const result = await runtime.run({
    name: "hello",
    image: "busybox",
    command: { path: "echo", args: ["Hello from PocketCI!"] },
  });
  console.log(result.stdout);
};

export { pipeline };
```

Narrate: *"A PocketCI pipeline is just a TypeScript async function. This one
runs a single container — the `busybox` image — and calls `echo`."*

<!-- PAUSE -->

---

## Scene 5 — Register the pipeline

```bash
pocketci pipeline set hello.ts \
  --server http://localhost:8080 \
  --name hello \
  --driver docker
```

**Expected output:**
```
pipeline "hello" set
```

<!-- PAUSE — switch to browser -->

---

## Scene 6 — Show the pipeline in the UI

Reload `http://localhost:8080/pipelines/` in the browser.

The `hello` pipeline now appears in the list. Narrate: *"The pipeline is stored
on the server. Now let's run it."*

<!-- PAUSE — switch back to terminal -->

---

## Scene 7 — Run the pipeline (streaming output)

```bash
pocketci pipeline run hello --server-url http://localhost:8080
```

**Expected output:**
```
Hello from PocketCI!
```

Narrate: *"`pipeline run` streams output directly to your terminal and waits
for the pipeline to finish — exit code and all."*

<!-- PAUSE -->

---

## Scene 8 — Trigger async + check the UI

```bash
pocketci pipeline trigger hello --server http://localhost:8080
```

**Expected output:**
```
{"runID":"..."}
```

Switch to the browser and navigate to:

```
http://localhost:8080/pipelines/hello
```

Show the completed run in the run history. Narrate: *"`pipeline trigger` is
fire-and-forget — you get a run ID back immediately, and you can track progress
in the UI."*

<!-- PAUSE -->

---

## Scene 9 — Closing shot

Switch back to the terminal. Run:

```bash
pocketci pipeline ls --server http://localhost:8080
```

Show the pipeline listed. End on the browser with the run history visible.

Narrate: *"That's it — a TypeScript pipeline, running in Docker, managed by
PocketCI. Check the docs to learn about webhooks, scheduling, secrets, and
more."*

---

## Full command reference (copy-paste sheet)

```bash
# 1. Install
brew install jtarchie/pocketci/pocketci

# 2. Start the server
pocketci server --port 8080 --storage-sqlite-path pocketci.db

# 3. Register the pipeline
pocketci pipeline set hello.ts --server http://localhost:8080 --name hello --driver docker

# 4. Run (synchronous — streams output)
pocketci pipeline run hello --server-url http://localhost:8080

# 5. Trigger (async — returns run ID)
pocketci pipeline trigger hello --server http://localhost:8080

# 6. List pipelines
pocketci pipeline ls --server http://localhost:8080
```
