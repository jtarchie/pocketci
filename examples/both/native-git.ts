/// <reference path="../../packages/pocketci/src/global.d.ts" />

// Example pipeline demonstrating native git resource usage
// This bypasses container-based resources for improved performance

const pipeline = async () => {
  // Check if git is available as a native resource
  const isGitNative = nativeResources.isNative("git");
  console.log(`Git native resource available: ${isGitNative}`);

  // List all available native resources
  const nativeResourceList = nativeResources.listNativeResources();
  console.log(`Available native resources: ${nativeResourceList.join(", ")}`);

  if (isGitNative) {
    // Use native git resource to check for versions
    console.log("Checking for git versions...");

    const checkResult = nativeResources.check({
      type: "git",
      source: {
        uri: "https://github.com/octocat/Hello-World.git",
        branch: "master",
      },
    });

    console.log(`Found ${checkResult.versions.length} version(s)`);

    if (checkResult.versions.length > 0) {
      const latestVersion = checkResult.versions[0];
      console.log(`Latest version: ${latestVersion.ref}`);

      // Create a volume for the git checkout
      const volume = await volumes.create({ name: "git-checkout" });

      // For a real implementation, you'd use the native In operation
      // For now, we'll just verify we can run a container with the volume
      const result = await runtime.run({
        name: "verify-git",
        image: "alpine/git:latest",
        command: {
          path: "git",
          args: [
            "clone",
            "--branch",
            "master",
            "--depth",
            "1",
            "https://github.com/octocat/Hello-World.git",
            "/workspace",
          ],
        },
        mounts: {
          "/workspace": volume,
        },
      });

      assert.equal(result.code, 0, "Git clone should succeed");

      // Verify the clone worked
      const verifyResult = await runtime.run({
        name: "verify-readme",
        image: "busybox",
        command: {
          path: "cat",
          args: ["/workspace/README"],
        },
        mounts: {
          "/workspace": volume,
        },
      });

      assert.containsString(verifyResult.stdout, "Hello World");
      console.log("Successfully cloned and verified repository!");
    }
  } else {
    console.log("Git native resource not available, falling back to container");
  }
};

export { pipeline };
