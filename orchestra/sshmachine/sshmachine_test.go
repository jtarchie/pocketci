package sshmachine_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtarchie/pocketci/orchestra/sshmachine"
	. "github.com/onsi/gomega"
	"golang.org/x/crypto/ssh"
)

func TestGenerateRSAKeyPairWritesPrivateKeyAndReturnsPublicKey(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	path := filepath.Join(t.TempDir(), "id_rsa")

	kp, err := sshmachine.GenerateRSAKeyPair(path)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(kp.PrivateKeyPath).To(Equal(path))
	assert.Expect(kp.PublicKeyAuthorized).To(HavePrefix("ssh-rsa "))

	signer, err := sshmachine.LoadSigner(path)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(signer).NotTo(BeNil())
}

func TestWaitForSSHRetriesUntilDialSucceeds(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	path := filepath.Join(t.TempDir(), "id_rsa")
	_, err := sshmachine.GenerateRSAKeyPair(path)
	assert.Expect(err).NotTo(HaveOccurred())

	signer, err := sshmachine.LoadSigner(path)
	assert.Expect(err).NotTo(HaveOccurred())

	var attempts atomic.Int64

	stub := func(_ string, _ string, _ *ssh.ClientConfig) (*ssh.Client, error) {
		if attempts.Add(1) < 3 {
			return nil, errors.New("connection refused")
		}

		return &ssh.Client{}, nil
	}

	restore := sshmachine.SetDialForTest(stub)
	defer restore()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := sshmachine.WaitForSSH(ctx, "10.0.0.1", sshmachine.WaitConfig{
		Signer:    signer,
		Timeout:   5 * time.Second,
		PollEvery: 10 * time.Millisecond,
	})
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(conn).NotTo(BeNil())
	assert.Expect(attempts.Load()).To(Equal(int64(3)))
}

func TestWaitForSSHRespectsDeadline(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	path := filepath.Join(t.TempDir(), "id_rsa")
	_, err := sshmachine.GenerateRSAKeyPair(path)
	assert.Expect(err).NotTo(HaveOccurred())

	signer, err := sshmachine.LoadSigner(path)
	assert.Expect(err).NotTo(HaveOccurred())

	stub := func(_ string, _ string, _ *ssh.ClientConfig) (*ssh.Client, error) {
		return nil, errors.New("connection refused")
	}

	restore := sshmachine.SetDialForTest(stub)
	defer restore()

	ctx := context.Background()

	_, err = sshmachine.WaitForSSH(ctx, "10.0.0.1", sshmachine.WaitConfig{
		Signer:    signer,
		Timeout:   100 * time.Millisecond,
		PollEvery: 10 * time.Millisecond,
	})
	assert.Expect(err).To(HaveOccurred())
	assert.Expect(err.Error()).To(ContainSubstring("timeout waiting for SSH"))
}

func TestWaitForSSHRequiresSigner(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	_, err := sshmachine.WaitForSSH(context.Background(), "10.0.0.1", sshmachine.WaitConfig{
		Timeout: time.Second,
	})
	assert.Expect(err).To(HaveOccurred())
}

func TestWaitForDockerRequiresClient(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	err := sshmachine.WaitForDocker(context.Background(), nil, nil, time.Second)
	assert.Expect(err).To(HaveOccurred())
}
