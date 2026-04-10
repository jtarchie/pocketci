package cache

import (
	"log/slog"

	"github.com/jtarchie/pocketci/orchestra"
)

// WrapWithCaching wraps a driver with caching using the provided store.
// If store is nil, the original driver is returned unchanged.
// compression is the compression algorithm ("zstd", "gzip", "none"); defaults to "zstd" when empty.
// keyPrefix is prepended to all cache keys.
func WrapWithCaching(
	driver orchestra.Driver,
	store CacheStore,
	compression string,
	keyPrefix string,
	logger *slog.Logger,
	volOpts ...CachingVolumeOption,
) orchestra.Driver {
	if store == nil {
		return driver
	}

	logger.Info("cache.initializing", "driver", driver.Name())

	if compression == "" {
		compression = "zstd"
	}

	compressor := NewCompressor(compression)

	if _, ok := driver.(VolumeDataAccessor); !ok {
		logger.Warn("driver.volumeDataAccess.unsupported", "driver", driver.Name())
		return driver
	}

	return NewCachingDriver(driver, store, compressor, keyPrefix, logger, volOpts...)
}

// AugmentKeyPrefix returns a copy of the CachingDriver with additionalPrefix appended
// to its existing key prefix. If driver is not a *CachingDriver (caching not configured),
// the original driver is returned unchanged.
//
// This is used to scope cache keys to a specific pipeline/job/task combination without
// changing the global prefix configured at startup.
func AugmentKeyPrefix(driver orchestra.Driver, additionalPrefix string) orchestra.Driver {
	cd, ok := driver.(*CachingDriver)
	if !ok {
		return driver
	}

	newPrefix := additionalPrefix
	if cd.keyPrefix != "" {
		newPrefix = cd.keyPrefix + "/" + additionalPrefix
	}

	return NewCachingDriver(cd.inner, cd.store, cd.compressor, newPrefix, cd.logger, cd.volOpts...)
}
