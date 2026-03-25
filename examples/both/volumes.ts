const pipeline = async () => {
  const volume = await volumes.create();
  let result = await runtime.run({
    name: "simple-task",
    image: "busybox",
    command: {
      path: "sh",
      args: ["-c", "echo Hello, World! > ./mounted-volume/hello.txt"],
    },
    mounts: {
      "mounted-volume": volume,
    },
  });
  console.log(JSON.stringify(result));
  assert.equal(result.code, 0);

  result = await runtime.run({
    name: "simple-task",
    image: "busybox",
    command: {
      path: "cat",
      args: ["./mounted-volume/hello.txt"],
    },
    mounts: {
      "mounted-volume": volume,
    },
  });
  console.log(JSON.stringify(result));
  assert.equal(result.code, 0);
  assert.containsString(result.stdout, "Hello, World!");
};

export { pipeline };
