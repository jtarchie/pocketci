// k6 load-testing pipeline.
//
// Usage:
//
//   ci pipeline set k6.ts --name k6 --server-url http://localhost:8080
//   ci pipeline run k6 run --vus=10 --duration=30s /workspace/script.js --server-url http://localhost:8080
//
// The client's current directory is uploaded to the server and mounted at
// /workspace inside the container, so any local script files are available.

const pipeline = async () => {
  const workdir = await runtime.createVolume({ name: "workdir", size: 200 });

  const result = await runtime.run({
    name: "k6",
    image: "grafana/k6:latest",
    command: {
      path: "k6",
      args: pipelineContext.args,
    },
    work_dir: "/workspace",
    mounts: {
      "/workspace": workdir,
    },
  });

  if (result.code !== 0) {
    throw new Error(`k6 exited with code ${result.code}:\n${result.stderr}`);
  }
};

export { pipeline };
