// sandbox.ts — demonstrates multi-command sandbox execution.
//
// A sandbox keeps a container alive across multiple sequential exec() calls,
// letting later commands observe state produced by earlier ones (files, etc.).
const pipeline = async () => {
  const sandbox = await runtime.startSandbox({
    name: "demo-sandbox",
    image: "busybox",
  });

  try {
    // First command: write a file inside the container.
    let result = await sandbox.exec({
      command: {
        path: "sh",
        args: ["-c", "echo hello-from-sandbox > /tmp/msg.txt"],
      },
    });
    assert.equal(result.code, 0);

    // Second command: read the file back — proves state is preserved.
    result = await sandbox.exec({
      command: { path: "cat", args: ["/tmp/msg.txt"] },
    });
    assert.equal(result.code, 0);
    assert.containsString(result.stdout, "hello-from-sandbox");

    // Third command: use per-exec env vars and workdir.
    result = await sandbox.exec({
      command: { path: "sh", args: ["-c", "echo $GREET && pwd"] },
      env: { GREET: "hey-world" },
      workDir: "/tmp",
    });
    assert.equal(result.code, 0);
    assert.containsString(result.stdout, "hey-world");
    assert.containsString(result.stdout, "/tmp");

    // Fourth command: non-zero exit code is captured.
    result = await sandbox.exec({
      command: { path: "sh", args: ["-c", "exit 2"] },
    });
    assert.equal(result.code, 2);
  } finally {
    await sandbox.close();
  }
};

export { pipeline };
