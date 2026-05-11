// Package sshmachine provides shared bootstrap helpers for orchestra drivers
// that provision a remote VM and reach it over plain SSH (DigitalOcean,
// Hetzner). It is not used by drivers with custom transports such as Fly's
// WireGuard tunnel + ed25519 SSH certificates.
package sshmachine

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	// DefaultPollInterval is the wait between SSH dial / docker ps attempts.
	DefaultPollInterval = 5 * time.Second
	// DefaultDialTimeout caps a single SSH dial attempt.
	DefaultDialTimeout = 10 * time.Second
)

// KeyPair is a freshly generated RSA-4096 key pair, with the private key
// already persisted to PrivateKeyPath (mode 0600) and the public key encoded
// in OpenSSH authorized_keys format.
type KeyPair struct {
	PrivateKeyPath      string
	PublicKeyAuthorized string
}

// GenerateRSAKeyPair creates a 4096-bit RSA key, writes the PEM private key to
// privateKeyPath with mode 0600, and returns both the path and the
// authorized_keys-format public key string. The caller is responsible for
// uploading PublicKeyAuthorized to the cloud provider.
func GenerateRSAKeyPair(privateKeyPath string) (KeyPair, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return KeyPair{}, fmt.Errorf("generate ssh key: %w", err)
	}

	privateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(priv),
	})

	err = os.WriteFile(privateKeyPath, privateKeyPEM, 0o600)
	if err != nil {
		return KeyPair{}, fmt.Errorf("write ssh private key: %w", err)
	}

	pub, err := ssh.NewPublicKey(&priv.PublicKey)
	if err != nil {
		return KeyPair{}, fmt.Errorf("derive ssh public key: %w", err)
	}

	return KeyPair{
		PrivateKeyPath:      privateKeyPath,
		PublicKeyAuthorized: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))),
	}, nil
}

// LoadSigner reads a PEM-encoded private key from disk and returns an
// ssh.Signer suitable for ssh.ClientConfig.Auth.
func LoadSigner(privateKeyPath string) (ssh.Signer, error) {
	data, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read ssh private key: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("parse ssh private key: %w", err)
	}

	return signer, nil
}

// WaitConfig controls the polling loop in WaitForSSH.
type WaitConfig struct {
	Logger      *slog.Logger
	Signer      ssh.Signer
	User        string
	Timeout     time.Duration
	PollEvery   time.Duration // defaults to DefaultPollInterval when zero
	DialTimeout time.Duration // defaults to DefaultDialTimeout when zero
}

// dial is overridable for tests so we can simulate retry behavior without a
// real SSH server.
var dial = func(network, addr string, config *ssh.ClientConfig) (*ssh.Client, error) {
	return ssh.Dial(network, addr, config)
}

// WaitForSSH polls until an SSH handshake against ip:22 succeeds or ctx /
// cfg.Timeout elapses. The returned *ssh.Client is owned by the caller, who
// must close it.
func WaitForSSH(ctx context.Context, ip string, cfg WaitConfig) (*ssh.Client, error) {
	if cfg.Signer == nil {
		return nil, errors.New("sshmachine: WaitForSSH requires a Signer")
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	if cfg.User == "" {
		cfg.User = "root"
	}

	if cfg.PollEvery <= 0 {
		cfg.PollEvery = DefaultPollInterval
	}

	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = DefaultDialTimeout
	}

	clientConfig := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(cfg.Signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // ephemeral CI machines have no stable host key
		Timeout:         cfg.DialTimeout,
	}

	deadline := time.Now().Add(cfg.Timeout)
	addr := net.JoinHostPort(ip, "22")

	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timeout waiting for SSH after %s", cfg.Timeout)
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for ssh: %w", ctx.Err())
		default:
		}

		conn, err := dial("tcp", addr, clientConfig)
		if err != nil {
			cfg.Logger.Debug("sshmachine.dial.error", "addr", addr, "err", err)

			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("wait for ssh: %w", ctx.Err())
			case <-time.After(cfg.PollEvery):
			}

			continue
		}

		cfg.Logger.Info("sshmachine.dial.ok", "addr", addr)

		return conn, nil
	}
}

// WaitForDocker polls `docker ps` over an established SSH client until it
// succeeds or timeout elapses.
func WaitForDocker(ctx context.Context, client *ssh.Client, logger *slog.Logger, timeout time.Duration) error {
	if client == nil {
		return errors.New("sshmachine: WaitForDocker requires an established ssh.Client")
	}

	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	deadline := time.Now().Add(timeout)

	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for Docker after %s", timeout)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for docker: %w", ctx.Err())
		default:
		}

		session, err := client.NewSession()
		if err != nil {
			logger.Debug("sshmachine.docker.session.error", "err", err)

			select {
			case <-ctx.Done():
				return fmt.Errorf("wait for docker: %w", ctx.Err())
			case <-time.After(DefaultPollInterval):
			}

			continue
		}

		output, err := session.CombinedOutput("docker ps")
		_ = session.Close()

		if err != nil {
			logger.Debug("sshmachine.docker.check.error", "err", err, "output", string(output))

			select {
			case <-ctx.Done():
				return fmt.Errorf("wait for docker: %w", ctx.Err())
			case <-time.After(DefaultPollInterval):
			}

			continue
		}

		logger.Info("sshmachine.docker.ok")

		return nil
	}
}
