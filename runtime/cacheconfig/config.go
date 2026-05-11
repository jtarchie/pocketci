// Package cacheconfig defines the S3 backend configuration shared by every
// caller that opts into the task-based cache lifecycle: the YAML/backwards
// runner that schedules cache_restore/cache_persist at job boundaries, and
// the JS runtime.cache namespace that lets TS pipelines invoke the same
// cache_op container task directly.
//
// The config struct is intentionally small. Multipart concurrency, part
// size, compression algorithm, and on-bucket TTL are not represented here:
// s5cmd inside the cache task chooses concurrency, the script hardcodes
// zstd, and TTL belongs on the bucket's lifecycle policy.
package cacheconfig

// S3 tells callers where to push and pull cache archives. AccessKeyID and
// SecretAccessKey may use the "secret:KEY" prefix; the runner resolves
// them at task launch the same way it does for any other secret env
// value.
type S3 struct {
	Endpoint        string
	Region          string
	Bucket          string
	Prefix          string // optional key prefix prepended to every cache key
	AccessKeyID     string
	SecretAccessKey string
}
