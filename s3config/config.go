// Package s3config provides shared S3 client configuration and option building
// used by all PocketCI S3 drivers (storage backend, volume cache, secrets).
//
// Configuration is constructed directly via the Config struct using individual
// fields populated from CLI flags or environment variables.
package s3config

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	tmtypes "github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Config holds the S3 configuration for connecting to an S3-compatible backend.
type Config struct {
	Bucket string
	Prefix string

	// AccessKeyID and SecretAccessKey are static credentials. When empty the SDK
	// credential chain (env vars, ~/.aws/credentials, IAM role, etc.) is used.
	AccessKeyID     string
	SecretAccessKey string

	// Region is the AWS region. Uses the SDK credential-chain default when empty.
	Region string

	// Endpoint is the custom S3-compatible base URL (MinIO, Cloudflare R2, etc.).
	Endpoint string

	// ForcePathStyle enables path-style S3 URLs (required for most non-AWS
	// endpoints). Defaults to true whenever Endpoint is set; can be overridden
	// with force_path_style=false when virtual-hosted-style is required (e.g.
	// Cloudflare R2 with custom domain).
	ForcePathStyle bool

	// EncryptMode is the server-side encryption mode.
	// Values: "" (none, default), "sse-s3" (AES-256), "sse-kms" (KMS), "sse-c" (customer-provided key).
	EncryptMode string

	// SSEKMSKeyID is the KMS key ID used when EncryptMode == "sse-kms".
	// When empty the provider's default KMS key is used.
	SSEKMSKeyID string

	// SSECKey is the 32-byte customer-provided key used when EncryptMode == "sse-c".
	// Derived as SHA-256 of the key= passphrase.
	SSECKey []byte

	// Key is the raw passphrase from the key= query parameter.
	// Used by the secrets driver for application-layer AES-256-GCM encryption,
	// and as the source material for SSECKey when EncryptMode == "sse-c".
	Key string

	// TTL is the optional cache expiry duration.
	// Only populated when the ttl query parameter is present.
	// Storage drivers ignore this field.
	TTL time.Duration
}

// LoadAWSConfig loads an AWS config using the SDK default credential chain,
// optionally overriding with static credentials when they were embedded in the DSN.
func (c *Config) LoadAWSConfig(ctx context.Context) (aws.Config, error) {
	var opts []func(*config.LoadOptions) error

	if c.AccessKeyID != "" && c.SecretAccessKey != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(c.AccessKeyID, c.SecretAccessKey, ""),
		))
	}

	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return awsCfg, nil
}

// ClientOptions returns the S3 client functional options derived from Region
// and Endpoint. Pass the result as variadic args to s3.NewFromConfig().
func (c *Config) ClientOptions() []func(*s3.Options) {
	var opts []func(*s3.Options)

	if c.Region != "" {
		region := c.Region
		opts = append(opts, func(o *s3.Options) {
			o.Region = region
		})
	}

	if c.Endpoint != "" {
		endpoint := c.Endpoint
		forcePathStyle := c.ForcePathStyle
		opts = append(opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = forcePathStyle
		})
	}

	return opts
}

// ssecStrings returns the base64-encoded key and key MD5 for SSE-C operations.
// Only valid when EncryptMode == "sse-c".
func (c *Config) ssecStrings() (algorithm, key, keyMD5 string) {
	b64key := base64.StdEncoding.EncodeToString(c.SSECKey)
	sum := md5.Sum(c.SSECKey) //nolint:gosec // SSE-C protocol requires MD5 of the key, not for security
	b64md5 := base64.StdEncoding.EncodeToString(sum[:])

	return "AES256", b64key, b64md5
}

// ApplySSEToPut sets server-side encryption fields on a PutObjectInput.
// No-op when EncryptMode is empty.
func (c *Config) ApplySSEToPut(input *s3.PutObjectInput) {
	switch c.EncryptMode {
	case "sse-s3":
		input.ServerSideEncryption = types.ServerSideEncryptionAes256
	case "sse-kms":
		input.ServerSideEncryption = types.ServerSideEncryptionAwsKms
		if c.SSEKMSKeyID != "" {
			input.SSEKMSKeyId = aws.String(c.SSEKMSKeyID)
		}
	case "sse-c":
		algorithm, key, keyMD5 := c.ssecStrings()
		input.SSECustomerAlgorithm = aws.String(algorithm)
		input.SSECustomerKey = aws.String(key)
		input.SSECustomerKeyMD5 = aws.String(keyMD5)
	}
}

// ApplySSEToGet sets SSE-C fields on a GetObjectInput.
// No-op when EncryptMode is not "sse-c" (server-managed modes need no headers on reads).
func (c *Config) ApplySSEToGet(input *s3.GetObjectInput) {
	if c.EncryptMode != "sse-c" {
		return
	}

	algorithm, key, keyMD5 := c.ssecStrings()
	input.SSECustomerAlgorithm = aws.String(algorithm)
	input.SSECustomerKey = aws.String(key)
	input.SSECustomerKeyMD5 = aws.String(keyMD5)
}

// ApplySSEToHead sets SSE-C fields on a HeadObjectInput.
// No-op when EncryptMode is not "sse-c".
func (c *Config) ApplySSEToHead(input *s3.HeadObjectInput) {
	if c.EncryptMode != "sse-c" {
		return
	}

	algorithm, key, keyMD5 := c.ssecStrings()
	input.SSECustomerAlgorithm = aws.String(algorithm)
	input.SSECustomerKey = aws.String(key)
	input.SSECustomerKeyMD5 = aws.String(keyMD5)
}

// ApplySSEToUpload sets server-side encryption fields on a transfermanager UploadObjectInput.
// No-op when EncryptMode is empty.
func (c *Config) ApplySSEToUpload(input *transfermanager.UploadObjectInput) {
	switch c.EncryptMode {
	case "sse-s3":
		input.ServerSideEncryption = tmtypes.ServerSideEncryptionAes256
	case "sse-kms":
		input.ServerSideEncryption = tmtypes.ServerSideEncryptionAwsKms
		if c.SSEKMSKeyID != "" {
			input.SSEKMSKeyID = aws.String(c.SSEKMSKeyID)
		}
	case "sse-c":
		algorithm, key, keyMD5 := c.ssecStrings()
		input.SSECustomerAlgorithm = aws.String(algorithm)
		input.SSECustomerKey = aws.String(key)
		input.SSECustomerKeyMD5 = aws.String(keyMD5)
	}
}
