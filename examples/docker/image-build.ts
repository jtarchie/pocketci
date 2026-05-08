// Demonstrates the image.build() runtime primitive: produce a tiny container
// image from an inline Dockerfile and verify the build succeeds.
//
// This example does not push (push=false) so it runs without a registry —
// see the runtime/runner integration test for the full build-and-push round
// trip.

const pipeline = async () => {
  const source = await volumes.create({ name: "source" });

  // Seed the input volume with a trivial Dockerfile.
  const seed = await runtime.run({
    name: "seed-context",
    image: "busybox:latest",
    command: {
      path: "sh",
      args: [
        "-c",
        "printf 'FROM busybox\\nRUN echo pocketci-built > /msg.txt\\n' > source/Dockerfile && cat source/Dockerfile",
      ],
    },
    mounts: { source: source },
  });
  assert.equal(seed.code, 0, "seed-context must succeed");

  // Build the image. push=false keeps the example self-contained — buildkit
  // produces the manifest in its local store and emits a digest in metadata.
  const built = await image.build({
    name: "build-app",
    context: "source",
    dockerfile: "Dockerfile",
    tag: "pocketci/example:latest",
    push: false,
    inputs: { source: source },
  });

  assert.equal(built.ref, "pocketci/example:latest", "ref echoes the tag");
  assert.notEqual(built.digest, "", "buildkit should report a digest");
  assert.containsString(built.digest, "sha256:", "digest is sha256-prefixed");
};

export { pipeline };
