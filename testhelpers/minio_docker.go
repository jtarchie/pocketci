package testhelpers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	gonanoid "github.com/matoous/go-nanoid/v2"
	"github.com/onsi/gomega"
)

// DockerMinio is a handle to a minio/minio container started for testing.
// Other containers on the same Docker host can reach it at Endpoint(); the
// host can reach it at HostEndpoint().
type DockerMinio struct {
	id       string
	hostPort int
	bridgeIP string
	bucket   string
	client   *client.Client
}

// StartDockerMinIO boots a minio/minio container in the background and
// creates a bucket, then returns a handle. Containers on the same Docker
// bridge network can reach the server at Endpoint() (e.g. "172.17.0.x:9000");
// the test host can reach it at HostEndpoint() ("http://localhost:<published>").
//
// Tests should skip when Docker is unavailable.
func StartDockerMinIO(t *testing.T) *DockerMinio {
	t.Helper()

	g := gomega.NewWithT(t)

	_, dockerErr := exec.LookPath("docker")
	if dockerErr != nil {
		t.Skip("docker not installed; skipping integration test")
	}

	ctx := context.Background()

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	g.Expect(err).NotTo(gomega.HaveOccurred(), "docker client")

	pullReader, err := cli.ImagePull(ctx, "minio/minio:latest", image.PullOptions{})
	g.Expect(err).NotTo(gomega.HaveOccurred(), "pull minio")

	_, _ = io.Copy(io.Discard, pullReader)
	_ = pullReader.Close()

	containerName := "pocketci-test-minio-" + strings.ToLower(gonanoid.MustGenerate("abcdefghijklmnopqrstuvwxyz0123456789", 12))
	bucket := "testcache" + strings.ToLower(gonanoid.MustGenerate("abcdefghijklmnopqrstuvwxyz0123456789", 16))

	resp, err := cli.ContainerCreate(
		ctx,
		&container.Config{
			Image:        "minio/minio:latest",
			Cmd:          []string{"server", "/data", "--quiet"},
			ExposedPorts: nat.PortSet{"9000/tcp": {}},
			Env: []string{
				"MINIO_ROOT_USER=minioadmin",
				"MINIO_ROOT_PASSWORD=minioadmin",
			},
		},
		&container.HostConfig{
			AutoRemove:      false,
			PublishAllPorts: true,
		},
		&network.NetworkingConfig{},
		nil,
		containerName,
	)
	g.Expect(err).NotTo(gomega.HaveOccurred(), "create minio container")

	err = cli.ContainerStart(ctx, resp.ID, container.StartOptions{})
	g.Expect(err).NotTo(gomega.HaveOccurred(), "start minio container")

	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = cli.ContainerRemove(stopCtx, resp.ID, container.RemoveOptions{Force: true})
		_ = cli.Close()
	})

	inspect, err := cli.ContainerInspect(ctx, resp.ID)
	g.Expect(err).NotTo(gomega.HaveOccurred(), "inspect minio container")

	bridgeIP := ""
	if bridgeNet, ok := inspect.NetworkSettings.Networks["bridge"]; ok && bridgeNet != nil {
		bridgeIP = bridgeNet.IPAddress
	}

	g.Expect(bridgeIP).NotTo(gomega.BeEmpty(), "minio container should have a bridge IP")

	hostPort := 0

	if bindings, ok := inspect.NetworkSettings.Ports["9000/tcp"]; ok && len(bindings) > 0 {
		port, err := strconv.Atoi(bindings[0].HostPort)
		g.Expect(err).NotTo(gomega.HaveOccurred(), "parse host port")

		hostPort = port
	}

	mc := &DockerMinio{
		id:       resp.ID,
		hostPort: hostPort,
		bridgeIP: bridgeIP,
		bucket:   bucket,
		client:   cli,
	}

	// Wait for MinIO to accept connections on the host-published port.
	g.Eventually(func() bool {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, mc.HostEndpoint()+"/minio/health/live", nil)
		if err != nil {
			return false
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}

		_ = resp.Body.Close()

		return resp.StatusCode == http.StatusOK
	}, "30s", "200ms").Should(gomega.BeTrue(), "minio should be healthy")

	// Create the test bucket via the AWS SDK (signed PUT against MinIO).
	createCtx, createCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer createCancel()

	awsCfg, err := awsconfig.LoadDefaultConfig(createCtx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("minioadmin", "minioadmin", "")),
	)
	g.Expect(err).NotTo(gomega.HaveOccurred(), "aws config")

	s3Client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(mc.HostEndpoint())
		o.UsePathStyle = true
	})

	_, err = s3Client.CreateBucket(createCtx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	g.Expect(err).NotTo(gomega.HaveOccurred(), "create bucket %q", bucket)

	return mc
}

// Endpoint returns "http://<bridge-ip>:9000" — the address other containers on
// the default Docker bridge network use to reach this MinIO.
func (m *DockerMinio) Endpoint() string {
	return fmt.Sprintf("http://%s:9000", m.bridgeIP)
}

// HostEndpoint returns "http://localhost:<published-port>" — useful for tests
// running on the Docker host.
func (m *DockerMinio) HostEndpoint() string {
	if m.hostPort == 0 {
		return ""
	}

	return fmt.Sprintf("http://localhost:%d", m.hostPort)
}

// Bucket returns the name of the test bucket.
func (m *DockerMinio) Bucket() string {
	return m.bucket
}

// AccessKeyID returns the MinIO root access key ID.
func (m *DockerMinio) AccessKeyID() string {
	return "minioadmin"
}

// SecretAccessKey returns the MinIO root secret access key.
func (m *DockerMinio) SecretAccessKey() string {
	return "minioadmin"
}
