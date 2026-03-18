# Storage Backends

PocketCI persists pipelines, runs, and task data using a pluggable storage
backend. The backend is selected via flags on the [server](../cli/server.md)
command.

## SQLite (default)

SQLite is the default backend. It stores all data in a single file with
full-text search (FTS5) for pipeline and run search queries.

| Flag                    | Env                      | Default       | Description                                                                       |
| ----------------------- | ------------------------ | ------------- | --------------------------------------------------------------------------------- |
| `--storage-sqlite-path` | `CI_STORAGE_SQLITE_PATH` | `pocketci.db` | Path to the SQLite database file. Use `:memory:` for ephemeral in-memory storage. |

```bash
pocketci server --storage-sqlite-path pocketci.db
```

## S3

The S3 backend stores all data as JSON objects in an S3-compatible bucket. It
works with AWS S3, MinIO, Cloudflare R2, and any S3-compatible object store. Set
`--storage-s3-bucket` to enable it; when set it takes precedence over SQLite.

| Flag                             | Env                               | Description                                                      |
| -------------------------------- | --------------------------------- | ---------------------------------------------------------------- |
| `--storage-s3-bucket`            | `CI_STORAGE_S3_BUCKET`            | S3 bucket name (required to use S3 backend)                      |
| `--storage-s3-endpoint`          | `CI_STORAGE_S3_ENDPOINT`          | S3-compatible endpoint URL (e.g. `http://localhost:9000`)        |
| `--storage-s3-region`            | `CI_STORAGE_S3_REGION`            | AWS region (uses SDK default when omitted)                       |
| `--storage-s3-access-key-id`     | `CI_STORAGE_S3_ACCESS_KEY_ID`     | S3 access key ID (uses SDK credential chain when omitted)        |
| `--storage-s3-secret-access-key` | `CI_STORAGE_S3_SECRET_ACCESS_KEY` | S3 secret access key                                             |
| `--storage-s3-prefix`            | `CI_STORAGE_S3_PREFIX`            | Key prefix to scope all objects (allows sharing a single bucket) |

### Data Layout

Objects are stored at the following paths within the bucket (after any
configured prefix):

| Data      | Key Pattern                              |
| --------- | ---------------------------------------- |
| Tasks     | `tasks/{namespace}/{key-hierarchy}.json` |
| Pipelines | `pipelines/by-id/{id}.json`              |
|           | `pipelines/by-name/{name}.json`          |
| Runs      | `runs/{id}.json`                         |

### Authentication

When `--storage-s3-access-key-id` and `--storage-s3-secret-access-key` are
provided they are used directly. Otherwise the AWS SDK credential chain is used
(environment variables, `~/.aws/credentials`, IAM role, etc.):

```bash
# Explicit credentials via flags
pocketci server \
  --storage-s3-bucket my-ci-bucket \
  --storage-s3-endpoint http://localhost:9000 \
  --storage-s3-region us-east-1 \
  --storage-s3-access-key-id minioadmin \
  --storage-s3-secret-access-key minioadmin

# SDK credential chain
export AWS_ACCESS_KEY_ID=your-key
export AWS_SECRET_ACCESS_KEY=your-secret
export AWS_REGION=us-east-1
pocketci server --storage-s3-bucket my-ci-bucket
```

### Search Behavior

Search uses S3 `ListObjectsV2` with prefix filtering. Unlike the SQLite backend,
full-text search is not available — queries match against object content using
simple substring matching after listing.

### Examples

**AWS S3:**

```bash
pocketci server \
  --storage-s3-bucket my-ci-bucket \
  --storage-s3-prefix production \
  --storage-s3-region us-west-2
```

**MinIO (local development):**

```bash
pocketci server \
  --storage-s3-bucket ci-data \
  --storage-s3-endpoint http://localhost:9000 \
  --storage-s3-region us-east-1 \
  --storage-s3-access-key-id minioadmin \
  --storage-s3-secret-access-key minioadmin
```

**Cloudflare R2:**

```bash
pocketci server \
  --storage-s3-bucket ci-data \
  --storage-s3-endpoint https://ACCOUNT_ID.r2.cloudflarestorage.com \
  --storage-s3-region auto \
  --storage-s3-access-key-id AKID \
  --storage-s3-secret-access-key SECRET
```

**Shared bucket with prefix isolation:**

```bash
# Team A
pocketci server \
  --storage-s3-bucket shared-bucket \
  --storage-s3-prefix team-a

# Team B
pocketci server \
  --storage-s3-bucket shared-bucket \
  --storage-s3-prefix team-b
```

### Trade-offs

| Feature     | SQLite           | S3                      |
| ----------- | ---------------- | ----------------------- |
| Search      | Full-text (FTS5) | Prefix + substring      |
| Latency     | Local disk       | Network round-trip      |
| Concurrency | Single writer    | Last-writer-wins        |
| Persistence | Local file       | Durable object store    |
| Scaling     | Single node      | Shared across instances |
