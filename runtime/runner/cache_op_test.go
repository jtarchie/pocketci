package runner

import (
	"strings"
	"testing"

	"github.com/onsi/gomega"
)

func TestValidateCacheOpInput(t *testing.T) {
	t.Parallel()

	g := gomega.NewWithT(t)

	g.Expect(validateCacheOpInput(CacheOpInput{Direction: CachePersistDirection})).To(gomega.MatchError(gomega.ContainSubstring("name is required")))
	g.Expect(validateCacheOpInput(CacheOpInput{Name: "x", Direction: CachePersistDirection})).To(gomega.MatchError(gomega.ContainSubstring("volume is required")))
	g.Expect(validateCacheOpInput(CacheOpInput{Name: "x", Volume: VolumeResult{Name: "v"}, Direction: CachePersistDirection})).To(gomega.MatchError(gomega.ContainSubstring("bucket is required")))
	g.Expect(validateCacheOpInput(CacheOpInput{Name: "x", Volume: VolumeResult{Name: "v"}, Bucket: "b", Direction: CachePersistDirection})).To(gomega.MatchError(gomega.ContainSubstring("key is required")))
	g.Expect(validateCacheOpInput(CacheOpInput{Name: "x", Volume: VolumeResult{Name: "v"}, Bucket: "b", Key: "k"})).To(gomega.MatchError(gomega.ContainSubstring("direction must be")))
	g.Expect(validateCacheOpInput(CacheOpInput{Name: "x", Volume: VolumeResult{Name: "v"}, Bucket: "b", Key: "k", Direction: CachePersistDirection})).To(gomega.Succeed())
}

func TestCacheOpScriptPersist(t *testing.T) {
	t.Parallel()

	g := gomega.NewWithT(t)

	in := CacheOpInput{
		Name:      "cache-var-lib-buildkit",
		Volume:    VolumeResult{Name: "cache-var-lib-buildkit"},
		Direction: CachePersistDirection,
		Endpoint:  "https://fly.storage.tigris.dev",
		Bucket:    "ci-tigris",
		Key:       "pipeline/job/cache-var-lib-buildkit.tar.gz",
	}

	script := cacheOpScript(in, "cache-var-lib-buildkit")

	g.Expect(script).To(gomega.ContainSubstring("set -eu"))
	g.Expect(script).To(gomega.ContainSubstring("tar cf - -C './cache-var-lib-buildkit' . | pigz | aws s3 cp -"))
	g.Expect(script).To(gomega.ContainSubstring("'s3://ci-tigris/pipeline/job/cache-var-lib-buildkit.tar.gz'"))
	g.Expect(script).To(gomega.ContainSubstring("--endpoint-url 'https://fly.storage.tigris.dev'"))
}

func TestCacheOpScriptRestore(t *testing.T) {
	t.Parallel()

	g := gomega.NewWithT(t)

	in := CacheOpInput{
		Name:      "cache-repo--git",
		Volume:    VolumeResult{Name: "cache-repo--git"},
		Direction: CacheRestoreDirection,
		Endpoint:  "https://fly.storage.tigris.dev",
		Bucket:    "ci-tigris",
		Key:       "pipeline/job/cache-repo--git.tar.gz",
	}

	script := cacheOpScript(in, "cache-repo--git")

	g.Expect(script).To(gomega.ContainSubstring("aws s3 cp 's3://ci-tigris/pipeline/job/cache-repo--git.tar.gz' -"))
	g.Expect(script).To(gomega.ContainSubstring("pigz -d | tar xf - -C './cache-repo--git'"))
	g.Expect(script).To(gomega.ContainSubstring("[cache] miss (no prior data)"))
	// A miss must exit non-zero so the /tasks UI flags a cold cache as failure.
	g.Expect(script).To(gomega.MatchRegexp(`\[cache\] miss.*\n\s*exit 1`))
	// A real transport error stays distinct from a miss (exit 2).
	g.Expect(script).To(gomega.MatchRegexp(`restore failed.*\n.*\n\s*exit 2`))
}

func TestCacheOpScriptNoEndpoint(t *testing.T) {
	t.Parallel()

	g := gomega.NewWithT(t)

	in := CacheOpInput{
		Name:      "cache",
		Volume:    VolumeResult{Name: "cache"},
		Direction: CachePersistDirection,
		Bucket:    "b",
		Key:       "k.tar.gz",
	}

	script := cacheOpScript(in, "cache")

	g.Expect(script).NotTo(gomega.ContainSubstring("--endpoint-url"))
	g.Expect(script).To(gomega.ContainSubstring("aws s3 cp - 's3://b/k.tar.gz'"))
}

func TestCacheOpEnvSecretsPassthrough(t *testing.T) {
	t.Parallel()

	g := gomega.NewWithT(t)

	in := CacheOpInput{
		Name:            "cache",
		Volume:          VolumeResult{Name: "cache"},
		Direction:       CachePersistDirection,
		Bucket:          "b",
		Key:             "k.tar.gz",
		AccessKeyID:     "secret:CACHE_AKID",
		SecretAccessKey: "secret:CACHE_SECRET",
		Region:          "us-west-2",
	}

	env := cacheOpEnv(in)

	g.Expect(env).To(gomega.HaveKeyWithValue("AWS_ACCESS_KEY_ID", "secret:CACHE_AKID"))
	g.Expect(env).To(gomega.HaveKeyWithValue("AWS_SECRET_ACCESS_KEY", "secret:CACHE_SECRET"))
	g.Expect(env).To(gomega.HaveKeyWithValue("AWS_REGION", "us-west-2"))
	g.Expect(env).To(gomega.HaveKeyWithValue("AWS_DEFAULT_REGION", "us-west-2"))
}

func TestCacheOpEnvDefaultRegion(t *testing.T) {
	t.Parallel()

	g := gomega.NewWithT(t)

	env := cacheOpEnv(CacheOpInput{Direction: CachePersistDirection})

	g.Expect(env).To(gomega.HaveKeyWithValue("AWS_REGION", "us-east-1"))
}

func TestCacheOpRunInputShape(t *testing.T) {
	t.Parallel()

	g := gomega.NewWithT(t)

	in := CacheOpInput{
		Name:       "cache-var-lib-buildkit",
		Volume:     VolumeResult{Name: "cache-var-lib-buildkit"},
		Direction:  CachePersistDirection,
		Endpoint:   "https://example.com",
		Bucket:     "b",
		Key:        "k.tar.gz",
		StorageKey: "/pipeline/run-1/jobs/job/cache/persist/cache-var-lib-buildkit",
		Limits: BuildImageLimits{
			CPU:     2,
			CPUKind: "performance",
			Memory:  2 * 1024 * 1024 * 1024,
		},
	}

	r := cacheOpRunInput(in)

	g.Expect(r.Image).To(gomega.Equal(CacheOpDefaultImage))
	g.Expect(r.Name).To(gomega.Equal("cache-var-lib-buildkit"))
	g.Expect(r.StorageKey).To(gomega.Equal("/pipeline/run-1/jobs/job/cache/persist/cache-var-lib-buildkit"))
	g.Expect(r.Mounts).To(gomega.HaveKey("cache-var-lib-buildkit"))
	g.Expect(r.Command.Path).To(gomega.Equal("sh"))
	g.Expect(r.Command.Args).To(gomega.HaveLen(2))
	g.Expect(r.Command.Args[0]).To(gomega.Equal("-c"))
	g.Expect(strings.Contains(r.Command.Args[1], "tar cf -")).To(gomega.BeTrue())
	g.Expect(r.ContainerLimits.CPU).To(gomega.Equal(int64(2)))
	g.Expect(r.ContainerLimits.CPUKind).To(gomega.Equal("performance"))
	g.Expect(r.ContainerLimits.Memory).To(gomega.Equal(int64(2 * 1024 * 1024 * 1024)))
}
