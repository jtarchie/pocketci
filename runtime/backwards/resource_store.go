package backwards

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jtarchie/pocketci/storage"
)

// StoredVersion represents a saved resource version with metadata.
type StoredVersion struct {
	Version   map[string]string `json:"version"`
	JobName   string            `json:"job_name"`
	FetchedAt string            `json:"fetched_at"`
}

// hashString computes a djb2-style 32-bit hash and returns it as a hex string.
// This matches the TypeScript implementation: Math.imul(h, 31) ^ charCode.
func hashString(s string) string {
	var h uint32 = 5381

	for _, ch := range s {
		h = h*31 ^ uint32(ch)
	}

	return fmt.Sprintf("%x", h)
}

func rvMetaKey(name string) string {
	return fmt.Sprintf("/rv/%s/meta", name)
}

func rvVersionKey(name string, index int) string {
	return fmt.Sprintf("/rv/%s/versions/%010d", name, index)
}

func rvDedupKey(name, versionJSON string) string {
	return fmt.Sprintf("/rv/%s/v/%s", name, hashString(versionJSON))
}

// safeStorageGet retrieves a key from storage, returning nil on ErrNotFound.
func safeStorageGet(ctx context.Context, store storage.Driver, key string) (storage.Payload, error) {
	payload, err := store.Get(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil //nolint:nilnil // mirrors TS safeStorageGet which returns null on missing keys
	}

	if err != nil {
		return nil, fmt.Errorf("storage get %q: %w", key, err)
	}

	return payload, nil
}

// versionToAnyMap converts map[string]string to map[string]any for storage.
func versionToAnyMap(v map[string]string) map[string]any {
	m := make(map[string]any, len(v))
	for k, val := range v {
		m[k] = val
	}

	return m
}

// payloadToStoredVersion extracts a StoredVersion from a storage.Payload.
func payloadToStoredVersion(p storage.Payload) *StoredVersion {
	if p == nil {
		return nil
	}

	sv := &StoredVersion{}

	if v, ok := p["version"].(map[string]any); ok {
		sv.Version = make(map[string]string, len(v))

		for k, val := range v {
			if s, ok := val.(string); ok {
				sv.Version[k] = s
			}
		}
	}

	if s, ok := p["job_name"].(string); ok {
		sv.JobName = s
	}

	if s, ok := p["fetched_at"].(string); ok {
		sv.FetchedAt = s
	}

	return sv
}

// getMetaCount reads the version count from the meta key, returning 0 if not found.
func getMetaCount(ctx context.Context, store storage.Driver, name string) (int, error) {
	meta, err := safeStorageGet(ctx, store, rvMetaKey(name))
	if err != nil {
		return 0, err
	}

	if meta == nil {
		return 0, nil
	}

	if count, ok := meta["count"].(float64); ok {
		return int(count), nil
	}

	return 0, nil
}

// SaveResourceVersion saves a resource version with deduplication.
// If the version already exists (by hash + JSON match), only mutable fields are updated.
func SaveResourceVersion(
	ctx context.Context,
	store storage.Driver,
	name string,
	version map[string]string,
	jobName string,
) error {
	versionJSON, err := json.Marshal(version)
	if err != nil {
		return fmt.Errorf("marshal version: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	dk := rvDedupKey(name, string(versionJSON))

	dedupEntry, err := safeStorageGet(ctx, store, dk)
	if err != nil {
		return err
	}

	if dedupEntry != nil {
		if existingJSON, ok := dedupEntry["version_json"].(string); ok && existingJSON == string(versionJSON) {
			// Known version — update mutable fields only.
			var index int
			if idx, ok := dedupEntry["index"].(float64); ok {
				index = int(idx)
			}

			vk := rvVersionKey(name, index)

			existing, err := safeStorageGet(ctx, store, vk)
			if err != nil {
				return err
			}

			if existing != nil {
				existing["job_name"] = jobName
				existing["fetched_at"] = now

				if err := store.Set(ctx, vk, existing); err != nil {
					return fmt.Errorf("update existing version: %w", err)
				}
			}

			return nil
		}
		// Hash collision — fall through and insert as new entry.
	}

	count, err := getMetaCount(ctx, store, name)
	if err != nil {
		return err
	}

	if err := store.Set(ctx, rvVersionKey(name, count), storage.Payload{
		"version":    versionToAnyMap(version),
		"job_name":   jobName,
		"fetched_at": now,
	}); err != nil {
		return fmt.Errorf("save version: %w", err)
	}

	if err := store.Set(ctx, dk, storage.Payload{
		"index":        count,
		"version_json": string(versionJSON),
	}); err != nil {
		return fmt.Errorf("save dedup entry: %w", err)
	}

	if err := store.Set(ctx, rvMetaKey(name), storage.Payload{
		"count": count + 1,
	}); err != nil {
		return fmt.Errorf("save meta: %w", err)
	}

	return nil
}

// GetLatestResourceVersion returns the most recently saved version, or nil if none exist.
func GetLatestResourceVersion(
	ctx context.Context,
	store storage.Driver,
	name string,
) (*StoredVersion, error) {
	count, err := getMetaCount(ctx, store, name)
	if err != nil {
		return nil, err
	}

	if count <= 0 {
		return nil, nil //nolint:nilnil // mirrors TS which returns null when no versions exist
	}

	payload, err := safeStorageGet(ctx, store, rvVersionKey(name, count-1))
	if err != nil {
		return nil, err
	}

	return payloadToStoredVersion(payload), nil
}

// ListResourceVersions returns up to limit versions (0 means all), ordered by insertion.
func ListResourceVersions(
	ctx context.Context,
	store storage.Driver,
	name string,
	limit int,
) ([]StoredVersion, error) {
	count, err := getMetaCount(ctx, store, name)
	if err != nil {
		return nil, err
	}

	actualCount := count
	if limit > 0 && limit < count {
		actualCount = limit
	}

	versions := make([]StoredVersion, 0, actualCount)

	for i := range actualCount {
		payload, err := safeStorageGet(ctx, store, rvVersionKey(name, i))
		if err != nil {
			return nil, err
		}

		if sv := payloadToStoredVersion(payload); sv != nil {
			versions = append(versions, *sv)
		}
	}

	return versions, nil
}

// GetVersionsAfter returns all versions saved after afterVersion.
// If afterVersion is nil or not found, all versions are returned.
func GetVersionsAfter(
	ctx context.Context,
	store storage.Driver,
	name string,
	afterVersion map[string]string,
) ([]StoredVersion, error) {
	if afterVersion == nil {
		return ListResourceVersions(ctx, store, name, 0)
	}

	count, err := getMetaCount(ctx, store, name)
	if err != nil {
		return nil, err
	}

	afterJSON, err := json.Marshal(afterVersion)
	if err != nil {
		return nil, fmt.Errorf("marshal afterVersion: %w", err)
	}

	afterIndex := -1

	for i := range count {
		payload, err := safeStorageGet(ctx, store, rvVersionKey(name, i))
		if err != nil {
			return nil, err
		}

		if sv := payloadToStoredVersion(payload); sv != nil {
			svJSON, err := json.Marshal(sv.Version)
			if err != nil {
				return nil, fmt.Errorf("marshal stored version: %w", err)
			}

			if string(svJSON) == string(afterJSON) {
				afterIndex = i

				break
			}
		}
	}

	if afterIndex == -1 {
		return ListResourceVersions(ctx, store, name, 0)
	}

	results := make([]StoredVersion, 0, count-afterIndex-1)

	for i := afterIndex + 1; i < count; i++ {
		payload, err := safeStorageGet(ctx, store, rvVersionKey(name, i))
		if err != nil {
			return nil, err
		}

		if sv := payloadToStoredVersion(payload); sv != nil {
			results = append(results, *sv)
		}
	}

	return results, nil
}
