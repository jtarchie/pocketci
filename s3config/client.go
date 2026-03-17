package s3config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// errKeyNotFound is the sentinel used by Client methods when a key is absent.
// External callers should use IsNotFound to test for this condition.
var errKeyNotFound = errors.New("key not found in S3")

// IsNotFound reports whether err indicates a missing S3 object. It recognises
// the wrapped sentinel returned by Client methods, raw AWS SDK error types
// (NoSuchKey, NotFound), and the string-based fallbacks emitted by some
// S3-compatible providers.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, errKeyNotFound) {
		return true
	}

	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}

	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return true
	}

	return strings.Contains(err.Error(), "NoSuchKey") || strings.Contains(err.Error(), "StatusCode: 404")
}

// Client wraps an *s3.Client with convenience methods that apply the bucket,
// prefix, and SSE configuration from a parsed Config. Embed *Client in domain
// driver structs to promote these methods and eliminate boilerplate.
type Client struct {
	s3Client *s3.Client
	bucket   string
	prefix   string
	cfg      *Config
}

// NewClient creates a new Client from a parsed Config.
func NewClient(ctx context.Context, cfg *Config) (*Client, error) {
	if cfg.EncryptMode != "" {
		switch cfg.EncryptMode {
		case "sse-s3", "sse-kms", "sse-c":
			// valid
		default:
			return nil, fmt.Errorf("unsupported encrypt value %q: must be sse-s3, sse-kms, or sse-c", cfg.EncryptMode)
		}
	}

	awsCfg, err := cfg.LoadAWSConfig(ctx)
	if err != nil {
		return nil, err
	}

	return &Client{
		s3Client: s3.NewFromConfig(awsCfg, cfg.ClientOptions()...),
		bucket:   cfg.Bucket,
		prefix:   cfg.Prefix,
		cfg:      cfg,
	}, nil
}

// Prefix returns the key prefix configured for this client.
func (c *Client) Prefix() string {
	return c.prefix
}

// FullKey prepends the configured prefix to key, returning the full S3 object key.
func (c *Client) FullKey(key string) string {
	if c.prefix == "" {
		return key
	}

	return c.prefix + "/" + key
}

// StripPrefix removes the configured prefix (and its trailing slash) from key,
// returning the logical path relative to the prefix root.
func (c *Client) StripPrefix(key string) string {
	if c.prefix == "" {
		return key
	}

	return strings.TrimPrefix(key, c.prefix+"/")
}

// ListKeys returns all S3 object keys whose key starts with prefix.
func (c *Client) ListKeys(ctx context.Context, prefix string) ([]string, error) {
	var keys []string

	paginator := s3.NewListObjectsV2Paginator(c.s3Client, &s3.ListObjectsV2Input{
		Bucket: aws.String(c.bucket),
		Prefix: aws.String(prefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list objects with prefix %q: %w", prefix, err)
		}

		for _, obj := range page.Contents {
			keys = append(keys, aws.ToString(obj.Key))
		}
	}

	return keys, nil
}

// GetBytes fetches the object at key and returns its body as a byte slice.
// Returns an error wrapping errKeyNotFound (detectable via IsNotFound) when the
// key does not exist.
func (c *Client) GetBytes(ctx context.Context, key string) ([]byte, error) {
	input := &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	}
	c.cfg.ApplySSEToGet(input)

	result, err := c.s3Client.GetObject(ctx, input)
	if err != nil {
		if IsNotFound(err) {
			return nil, fmt.Errorf("get %q: %w", key, errKeyNotFound)
		}

		return nil, fmt.Errorf("failed to get object %q: %w", key, err)
	}

	defer func() { _ = result.Body.Close() }()

	data, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read object %q: %w", key, err)
	}

	return data, nil
}

// GetStream fetches the object at key and returns the raw SDK output for
// streaming. The caller is responsible for closing result.Body.
// Returns an error wrapping errKeyNotFound (detectable via IsNotFound) when the
// key does not exist.
func (c *Client) GetStream(ctx context.Context, key string) (*s3.GetObjectOutput, error) {
	input := &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	}
	c.cfg.ApplySSEToGet(input)

	result, err := c.s3Client.GetObject(ctx, input)
	if err != nil {
		if IsNotFound(err) {
			return nil, fmt.Errorf("get %q: %w", key, errKeyNotFound)
		}

		return nil, fmt.Errorf("failed to get object %q: %w", key, err)
	}

	return result, nil
}

// PutBytes uploads data to key using the transfer manager with SSE applied.
func (c *Client) PutBytes(ctx context.Context, key string, data []byte, contentType string) error {
	uploader := transfermanager.New(c.s3Client)

	input := &transfermanager.UploadObjectInput{
		Bucket:      aws.String(c.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String(contentType),
	}
	c.cfg.ApplySSEToUpload(input)

	if _, err := uploader.UploadObject(ctx, input); err != nil {
		return fmt.Errorf("failed to put object %q: %w", key, err)
	}

	return nil
}

// PutStream uploads the content of reader to key using the transfer manager.
// Additional transfermanager options (e.g. custom part size, concurrency) can
// be supplied via opts.
func (c *Client) PutStream(ctx context.Context, key string, reader io.Reader, opts ...func(*transfermanager.Options)) error {
	uploader := transfermanager.New(c.s3Client, opts...)

	input := &transfermanager.UploadObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
		Body:   reader,
	}
	c.cfg.ApplySSEToUpload(input)

	if _, err := uploader.UploadObject(ctx, input); err != nil {
		return fmt.Errorf("failed to put stream to %q: %w", key, err)
	}

	return nil
}

// HeadKey retrieves metadata for the object at key without fetching its body.
// Returns an error wrapping errKeyNotFound (detectable via IsNotFound) when the
// key does not exist.
func (c *Client) HeadKey(ctx context.Context, key string) (*s3.HeadObjectOutput, error) {
	input := &s3.HeadObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	}
	c.cfg.ApplySSEToHead(input)

	result, err := c.s3Client.HeadObject(ctx, input)
	if err != nil {
		if IsNotFound(err) {
			return nil, fmt.Errorf("head %q: %w", key, errKeyNotFound)
		}

		return nil, fmt.Errorf("failed to head object %q: %w", key, err)
	}

	return result, nil
}

// DeleteKey removes the object at key.
func (c *Client) DeleteKey(ctx context.Context, key string) error {
	_, err := c.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("failed to delete object %q: %w", key, err)
	}

	return nil
}
