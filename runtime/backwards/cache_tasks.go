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
	parts = append(parts, volumeName+".tar.gz")

	return strings.Join(parts, "/")
}

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
	}
}

// runCacheRestoreTask runs a cache_restore task for a single volume the
// first time the volume is created in a job. A miss exits zero — downstream
// tasks see an empty cache directory and proceed normally.
//
// Restore failures are logged but do not abort the job: the consuming task
// will start with an empty (or partially filled) cache, which is degraded
// but not broken.
func runCacheRestoreTask(sc *StepContext, volName string) {
	if sc.CacheS3 == nil {
		return
	}

	if sc.CacheRestored[volName] {
		return
	}

	sc.CacheRestored[volName] = true

	in := cacheOpInputBase(sc, volName)
	in.Direction = pipelinerunner.CacheRestoreDirection
	in.StorageKey = fmt.Sprintf("%s/cache/restore/%s", sc.BaseStorageKey(), volName)

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
