package backwards

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	pipelinerunner "github.com/jtarchie/pocketci/runtime/runner"
)

// cacheS3Key returns the S3 object key for one volume in a job.
//
//	[<prefix>/]<sanitizedPipelineID>/<sanitizedJobName>/<volumeName>.tar.gz
//
// Volume names are already sanitized (alphanum + hyphen) by sanitizeCachePath
// at creation time, so we don't re-sanitize them here.
func cacheS3Key(cfg *CacheS3Config, pipelineID, jobName, volumeName string) string {
	parts := make([]string, 0, 4)
	if cfg.Prefix != "" {
		parts = append(parts, strings.Trim(cfg.Prefix, "/"))
	}

	parts = append(parts, sanitizeCachePath(pipelineID))
	parts = append(parts, sanitizeCachePath(jobName))
	parts = append(parts, volumeName+".tar.zst")

	return strings.Join(parts, "/")
}

// cacheOpDefaultMemory is the memory floor (1 GiB) given to every cache
// task. The smallest Fly machine size (256 MiB) OOM-kills `dnf install
// tar zstd` on the slim amazon/aws-cli base image, so we ensure cache
// tasks always get a bigger machine even when no caller-supplied
// limits override.
const cacheOpDefaultMemory int64 = 1024 * 1024 * 1024

// cacheOpDefaultCPU is the CPU count given to every cache task.
// zstd -T0 scales near-linearly with cores on compress-bound caches
// (multi-GB /var/lib/buildkit), so 4 cores roughly quarters wall-clock
// time versus single-core compression. CPUKind defaults to "shared" —
// cache tasks are bursty and finite, so we don't reserve performance
// cores.
const cacheOpDefaultCPU int64 = 4

// cacheOpInputBase returns a CacheOpInput pre-filled with the S3 config
// and volume metadata for a job-level cache op. Caller sets Direction
// and StorageKey.
func cacheOpInputBase(sc *StepContext, volName string) pipelinerunner.CacheOpInput {
	cfg := sc.CacheS3

	return pipelinerunner.CacheOpInput{
		Name: volName,
		Volume: pipelinerunner.VolumeResult{
			Name: volName,
		},
		Endpoint:        cfg.Endpoint,
		Region:          cfg.Region,
		Bucket:          cfg.Bucket,
		Key:             cacheS3Key(cfg, sc.PipelineID, sc.JobName, volName),
		AccessKeyID:     cfg.AccessKeyID,
		SecretAccessKey: cfg.SecretAccessKey,
		OnOutput:        sc.OutputCallback,
		Limits: pipelinerunner.BuildImageLimits{
			CPU:     cacheOpDefaultCPU,
			CPUKind: "shared",
			Memory:  cacheOpDefaultMemory,
		},
	}
}

// runCacheRestoreTask runs a cache_restore task for a single volume the
// first time the volume is created in a job. A cache miss exits non-zero
// (script exit code 1) so the /tasks UI shows the restore as failure and
// makes it visually obvious whether the cache was warm. The job continues
// regardless: the consuming task starts with an empty cache and
// repopulates it.
//
// stepPathPrefix is the consuming step's storage prefix (e.g. "0",
// "1/on_success"). The restore's storage path lives under that prefix so
// the /tasks tree renders cache_restore immediately above the task it
// precedes.
func runCacheRestoreTask(sc *StepContext, volName, stepPathPrefix string) {
	if sc.CacheS3 == nil {
		return
	}

	if sc.CacheRestored[volName] {
		return
	}

	sc.CacheRestored[volName] = true

	in := cacheOpInputBase(sc, volName)
	in.Direction = pipelinerunner.CacheRestoreDirection
	in.StorageKey = fmt.Sprintf("%s/%s/cache/restore/%s", sc.BaseStorageKey(), stepPathPrefix, volName)

	sc.Logger.Info("cache.restore.start",
		slog.String("volume", volName),
		slog.String("key", in.Key),
	)

	started := time.Now()

	_, err := pipelinerunner.CacheRestore(sc.PipelineRunner, in)

	elapsed := time.Since(started)

	if err != nil {
		sc.Logger.Warn("cache.restore.failed",
			slog.String("volume", volName),
			slog.Duration("elapsed", elapsed),
			slog.Any("error", err),
		)

		return
	}

	sc.Logger.Info("cache.restore.done",
		slog.String("volume", volName),
		slog.Duration("elapsed", elapsed),
	)
}

// runCachePersistTasks runs cache_persist tasks for every volume tracked
// in sc.CacheVolumeObjects. Persist failures are logged but do not abort
// the deferred cleanup; the next run starts with a stale or empty cache.
func runCachePersistTasks(sc *StepContext) {
	if sc.CacheS3 == nil || len(sc.CacheVolumeObjects) == 0 {
		return
	}

	volNames := make([]string, 0, len(sc.CacheVolumeObjects))
	for _, vol := range sc.CacheVolumeObjects {
		volNames = append(volNames, vol.Name())
	}

	sc.Logger.Info("cache.persist.start",
		slog.Int("count", len(volNames)),
		slog.Any("volumes", volNames),
	)

	for _, vol := range sc.CacheVolumeObjects {
		volName := vol.Name()

		in := cacheOpInputBase(sc, volName)
		in.Direction = pipelinerunner.CachePersistDirection
		in.StorageKey = fmt.Sprintf("%s/cache/persist/%s", sc.BaseStorageKey(), volName)

		started := time.Now()

		_, err := pipelinerunner.CachePersist(sc.PipelineRunner, in)

		elapsed := time.Since(started)

		if err != nil {
			sc.Logger.Warn("cache.persist.failed",
				slog.String("volume", volName),
				slog.Duration("elapsed", elapsed),
				slog.Any("error", err),
			)

			continue
		}

		sc.Logger.Info("cache.persist.done",
			slog.String("volume", volName),
			slog.Duration("elapsed", elapsed),
		)
	}
}
