# Volume Caching

The CI system supports transparent volume caching backed by S3-compatible
storage. Caches persist data across pipeline runs, making subsequent runs faster
by restoring previously computed artifacts, dependencies, or build outputs.

## How It Works

1. **Volume Creation**: When a pipeline creates a volume (directly or via
   `caches` in YAML), the system checks if a cached version exists in S3.
2. **Cache Restore**: If found, the cached data is downloaded, decompressed, and
   extracted into the volume before the task runs.
3. **Cache Persist**: When the pipeline completes, all volumes are persisted
   back to S3 with compression.

This is transparent to the pipeline — volumes behave identically whether caching
is enabled or not.

## Configuration

Caching is enabled on the server by setting `--cache-s3-bucket`. All cache
configuration is done via server flags or environment variables.

| Flag                           | Env                             | Default | Description                                               |
| ------------------------------ | ------------------------------- | ------- | --------------------------------------------------------- |
| `--cache-s3-bucket`            | `CI_CACHE_S3_BUCKET`            | —       | S3 bucket for cache storage (required to enable cache)    |
| `--cache-s3-endpoint`          | `CI_CACHE_S3_ENDPOINT`          | —       | S3-compatible endpoint URL (e.g. `http://localhost:9000`) |
| `--cache-s3-region`            | `CI_CACHE_S3_REGION`            | —       | AWS region                                                |
| `--cache-s3-access-key-id`     | `CI_CACHE_S3_ACCESS_KEY_ID`     | —       | S3 access key ID                                          |
| `--cache-s3-secret-access-key` | `CI_CACHE_S3_SECRET_ACCESS_KEY` | —       | S3 secret access key                                      |
| `--cache-s3-prefix`            | `CI_CACHE_S3_PREFIX`            | —       | Key prefix for all cache entries                          |
| `--cache-s3-ttl`               | `CI_CACHE_S3_TTL`               | `0`     | Cache expiration duration (0 = no expiry, e.g. `24h`)     |
| `--cache-compression`          | `CI_CACHE_COMPRESSION`          | `zstd`  | Compression algorithm: `zstd`, `gzip`, or `none`          |
| `--cache-key-prefix`           | `CI_CACHE_KEY_PREFIX`           | —       | Logical key prefix within the bucket                      |

## Full Examples

### AWS S3

```bash
pocketci server \
  --cache-s3-bucket my-pocketci-cache \
  --cache-s3-region us-west-2 \
  --cache-key-prefix project-a
```

### Cloudflare R2

```bash
pocketci server \
  --cache-s3-bucket cache-bucket \
  --cache-s3-endpoint https://ACCOUNT_ID.r2.cloudflarestorage.com \
  --cache-s3-region auto \
  --cache-s3-access-key-id AKID \
  --cache-s3-secret-access-key SECRET \
  --cache-key-prefix project-a
```

### MinIO (Local S3-Compatible)

```bash
# Start MinIO locally
docker run -p 9000:9000 -p 9001:9001 \
  -e MINIO_ROOT_USER=minioadmin \
  -e MINIO_ROOT_PASSWORD=minioadmin \
  minio/minio server /data --console-address ":9001"

# Create bucket
aws --endpoint-url http://localhost:9000 s3 mb s3://cache-bucket

# Start server with caching
pocketci server \
  --cache-s3-bucket cache-bucket \
  --cache-s3-endpoint http://localhost:9000 \
  --cache-s3-region us-east-1 \
  --cache-s3-access-key-id minioadmin \
  --cache-s3-secret-access-key minioadmin
```

### With Compression Options

```bash
# Use gzip instead of zstd
pocketci server \
  --cache-s3-bucket my-cache \
  --cache-compression gzip

# Disable compression (faster for already-compressed data)
pocketci server \
  --cache-s3-bucket my-cache \
  --cache-compression none
```

## YAML Pipeline Usage

Use the `caches` field in task configs to define cache directories:

```yaml
jobs:
  - name: build
    plan:
      - task: install-deps
        config:
          platform: linux
          image_resource:
            type: registry-image
            source:
              repository: node:20
          caches:
            - path: node_modules
            - path: .npm
          run:
            path: sh
            args:
              - -c
              - |
                  npm ci
                  npm run build
```

### Cache Behavior

- **Path**: Relative to the task's working directory
- **Name**: Derived from the path (e.g., `node_modules` → `cache-node_modules`)
- **Sharing**: Caches with the same name share data across tasks in the same
  pipeline run
- **Persistence**: Caches are uploaded to S3 when the pipeline completes

### Multiple Caches

```yaml
caches:
  - path: .cache/go-build # Go build cache
  - path: .cache/golangci # Linter cache
  - path: vendor # Vendored dependencies
```

## TypeScript/JavaScript Usage

For direct JS/TS pipelines, create named volumes:

```typescript
const pipeline = async () => {
  // Create a cached volume
  const cache = await runtime.createVolume({ name: "build-cache" });

  // Use the volume in a task
  await runtime.run({
    name: "build",
    image: "node:20",
    command: { path: "npm", args: ["run", "build"] },
    mounts: [{ name: cache.name, path: "node_modules" }],
  });
};

export { pipeline };
```

## Supported Drivers

Caching works with drivers that implement `VolumeDataAccessor`:

| Driver   | Caching Support | Notes                                      |
| -------- | --------------- | ------------------------------------------ |
| `docker` | ✅ Yes          | Uses `docker cp` for volume data transfer  |
| `native` | ✅ Yes          | Uses tar directly on the filesystem        |
| `k8s`    | ✅ Yes          | Uses a helper pod for volume data transfer |

## Cache Key Structure

Cache keys are structured as:

```
{cache_prefix}/{volume_name}.tar.{compression}
```

Examples:

- `myproject/cache-node_modules.tar.zst`
- `build-cache.tar.zst` (no prefix)
- `pocketci/main/vendor.tar.gzip`

## Environment Variables

AWS credentials can be provided via standard AWS SDK environment variables:

```bash
export AWS_ACCESS_KEY_ID=your-key
export AWS_SECRET_ACCESS_KEY=your-secret
export AWS_REGION=us-east-1

pocketci server --cache-s3-bucket my-cache
```

Or use IAM roles, instance profiles, or other AWS SDK credential sources.

## Troubleshooting

### Cache Not Being Restored

1. **Check cache key**: Ensure `cache_prefix` and volume names match between
   runs
2. **Verify S3 access**: Check AWS credentials and bucket permissions
3. **Check logs**: Look for "cache miss" or "restoring volume from cache"
   messages

### Cache Not Being Persisted

1. **Pipeline must complete**: Caches are persisted when the pipeline finishes
2. **Check S3 write permissions**: Ensure the credentials allow `PutObject`
3. **Check logs**: Look for "persisting volume to cache" messages

### Performance Tips

- Use `zstd` compression (default) for best speed/ratio balance
- Use `none` compression for already-compressed data (tar.gz archives, etc.)
- Set appropriate `ttl` to automatically expire stale caches
- Use specific cache paths rather than caching entire directories
