#!/bin/sh
set -eu

# Map Tigris (fly.io storage) env vars → PocketCI cache S3 config.
# Tigris sets: BUCKET_NAME, AWS_ENDPOINT_URL_S3, AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY
#
# Each of these may be unset (pocketci is also deployed without Tigris),
# so the `-n "${VAR:-}"` form is required to stay compatible with `set -u`.

if [ -n "${BUCKET_NAME:-}" ]; then
  export CI_CACHE_S3_BUCKET="${CI_CACHE_S3_BUCKET:-$BUCKET_NAME}"
fi
if [ -n "${AWS_ENDPOINT_URL_S3:-}" ]; then
  export CI_CACHE_S3_ENDPOINT="${CI_CACHE_S3_ENDPOINT:-$AWS_ENDPOINT_URL_S3}"
fi
if [ -n "${AWS_ACCESS_KEY_ID:-}" ]; then
  export CI_CACHE_S3_ACCESS_KEY_ID="${CI_CACHE_S3_ACCESS_KEY_ID:-$AWS_ACCESS_KEY_ID}"
fi
if [ -n "${AWS_SECRET_ACCESS_KEY:-}" ]; then
  export CI_CACHE_S3_SECRET_ACCESS_KEY="${CI_CACHE_S3_SECRET_ACCESS_KEY:-$AWS_SECRET_ACCESS_KEY}"
fi

exec "$@"
