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

	return NewCachingDriver(driver, store, compressor, keyPrefix, logger)
}
