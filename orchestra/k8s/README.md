# Kubernetes (K8s) Orchestra Driver

This driver implements the Orchestra container orchestration interface for
Kubernetes clusters.

## Overview

The K8s driver allows Orchestra to run tasks as Kubernetes Pods, providing
container orchestration through the Kubernetes API. It's designed to work with
any Kubernetes cluster, including local development clusters like minikube.

## Configuration

The driver uses standard Kubernetes client configuration:

1. **In-cluster configuration**: When running inside a Kubernetes cluster, it
   automatically uses the service account credentials
2. **Kubeconfig file**: When running outside a cluster, it uses the default
   kubeconfig file (`~/.kube/config`)

### Configuration

The K8s driver is configured via the `--driver` flag:

```bash
# Simple usage (uses 'default' namespace)
go run main.go runner --driver=k8s examples/both/hello-world.ts
```

**Available Parameters** (set via server CLI flags or set-pipeline driver config):

- `namespace` - Kubernetes namespace for resource placement (default: `default`)
- `kubeconfig` - Path to kubeconfig file (default: `~/.kube/config` or
  `KUBECONFIG` env var)

**Authentication Priority**:

1. In-cluster service account credentials (when running inside Kubernetes)
2. Kubeconfig from driver config (`kubeconfig=...`)
3. Kubeconfig from `KUBECONFIG` environment variable
4. Default kubeconfig at `~/.kube/config`

## Features

### Supported

- ✅ Container execution (Pods)
- ✅ Volume mounts (PersistentVolumeClaims)
- ✅ Environment variables
- ✅ Resource limits (CPU and memory)
- ✅ Exit code detection
- ✅ Log retrieval (stdout/stderr)
- ✅ Container cleanup
- ✅ Privileged containers
- ✅ Custom user (via security context, numeric UIDs only)
- ✅ Stdin support (via SPDY attach protocol)
- ✅ Idempotent operations (running same task ID returns existing pod)

### Known Limitations

- ⚠️ **User specification**: Only numeric UIDs are supported (e.g., `"65534"`
  for nobody). The special case `"root"` is mapped to UID 0. Username strings
  like `"nobody"` are not supported and will log a warning before falling back
  to the container's default user.
- ⚠️ **Stderr separation**: Kubernetes logs don't separate stdout and stderr by
  default. This requires the `PodLogsQuerySplitStreams` feature gate (alpha in
  Kubernetes 1.32+). Without this feature gate enabled, all logs are written to
  the stdout writer. See the "Feature Gates" section below for setup
  instructions.
- ✅ **Namespace**: Resources are created in the Kubernetes namespace specified
  by the `namespace` config parameter (defaults to `default`).
- ⚠️ **Storage classes**: PVCs use the default storage class. Custom storage
  class selection is not supported.

## Feature Gates

### PodLogsQuerySplitStreams (Kubernetes 1.32+)

The driver supports separate stdout/stderr log streams when the
`PodLogsQuerySplitStreams` feature gate is enabled. This alpha feature allows
proper separation of stdout and stderr, which is important for tasks that parse
JSON from stdout.

**To enable in minikube:**

```bash
# Delete existing cluster (if any)
minikube delete

# Start with feature gate enabled
minikube start --feature-gates=PodLogsQuerySplitStreams=true
```

**To verify the feature gate is enabled:**

```bash
kubectl get pod kube-apiserver-minikube -n kube-system -o yaml | grep feature-gates
```

**Behavior without the feature gate:**

- Stdout and stderr are combined in the log stream
- Applications that write debug logs to stderr may interfere with stdout parsing
- The driver falls back to writing all logs to the stdout writer

**Behavior with the feature gate:**

- Stdout and stderr are properly separated
- JSON and other structured output on stdout is not mixed with stderr logs
- Better compatibility with Concourse CI resource types and similar tools

## Resource Naming

The driver sanitizes resource names to comply with Kubernetes naming
requirements:

- **Pod names**: Converted to lowercase, invalid characters replaced with
  hyphens, max 253 characters (DNS-1123 subdomain format)
- **PVC names**: Same as pod names
- **Labels**: Alphanumeric characters, hyphens, underscores, and dots allowed;
  must start/end with alphanumeric; max 63 characters

## Volume Support

Volumes are implemented as PersistentVolumeClaims (PVCs):

- Default access mode: `ReadWriteOnce`
- Default size: 1Gi (if size is 0 or not specified)
- Size conversion: Specified in bytes, converted to MiB for Kubernetes
- Storage class: Uses cluster default
- Volumes are mounted at `/tmp/{pod-name}/{mount-path}`
- Idempotent: Requesting the same volume name returns the existing PVC

## Resource Limits

CPU and memory limits are translated from Docker format to Kubernetes format:

- **CPU**: Docker CPU shares are converted to Kubernetes millicores using the
  formula: `(shares * 1000) / 1024`. For example, 512 shares becomes ~500
  millicores.
- **Memory**: Direct byte-to-byte mapping
- **Requests**: Kubernetes resource requests are automatically set to 50% of the
  specified limits for both CPU and memory
- **Zero values**: If CPU or Memory is 0, no limits are set (unlimited)

## Testing with Minikube

The driver is tested with minikube. To run tests locally:

```bash
# Start minikube with required feature gates
minikube start --feature-gates=PodLogsQuerySplitStreams=true

# Run k8s driver tests
go test -v -race -count=1 -run 'TestDrivers/k8s' ./orchestra

# Run all examples with k8s driver
go test -v -race -count=1 -run 'TestExamplesDocker/k8s:' ./examples

# Cleanup minikube
minikube delete
```

**Note**: The `PodLogsQuerySplitStreams` feature gate is required for all tests
to pass. Without it, tests that rely on clean stdout output (like resource
tests) will fail due to stderr logs being mixed into stdout.

## Implementation Notes

### Cleanup Strategy

The `Close()` method deletes all Pods and PVCs with the `orchestra.namespace`
label matching the driver's namespace. This ensures proper cleanup even if
individual `Cleanup()` calls were skipped.

### Idempotency

Running the same task (by task ID) multiple times returns the existing Pod/PVC
rather than creating duplicates. This matches the behavior of other Orchestra
drivers.

### Pod Lifecycle

- Pods use `RestartPolicy: Never`
- Pods are created and started immediately
- Stdin attachment (if provided) waits for pod to reach Running state with a
  30-second timeout
- Status polling detects when pods complete (Succeeded or Failed phase)
- Pods remain after completion for log retrieval
- Explicit cleanup or driver `Close()` removes pods

### Stdin Support

The driver implements stdin streaming using the Kubernetes attach protocol:

1. Pod is created with `Stdin: true` and `StdinOnce: true` if stdin is provided
2. Driver waits for pod to reach Running state (with container actually running,
   not just created)
3. Uses SPDY executor to attach stdin stream to the pod
4. Supports quick-completing pods that finish before Running state is reached
5. 30-second timeout for pod to become ready for stdin attachment

## Future Enhancements

Potential improvements for future versions:

1. **Username support**: Add username-to-UID resolution (requires querying
   container image or maintaining a mapping)
2. **Multi-namespace**: Support custom Kubernetes namespaces
3. **Storage classes**: Allow configurable storage class for PVCs
4. **Volume modes**: Support ReadWriteMany and other access modes
5. **Node selection**: Support node selectors and affinity rules
6. **Image pull secrets**: Support private container registries
7. **Health checks**: Add liveness/readiness probes
8. **Job resources**: Option to use Kubernetes Jobs instead of bare Pods
9. **Working directory**: Allow custom working directory (currently always
   `/tmp/{pod-name}`)

## Dependencies

- `k8s.io/client-go` - Kubernetes Go client library
- `k8s.io/api` - Kubernetes API types
- `k8s.io/apimachinery` - Kubernetes API machinery

## See Also

- [Kubernetes API Documentation](https://kubernetes.io/docs/reference/kubernetes-api/)
- [client-go Documentation](https://github.com/kubernetes/client-go)
- [Orchestra Driver Implementation Guide](../docs/implementing-new-driver.md)
