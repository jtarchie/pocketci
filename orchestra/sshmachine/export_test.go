package sshmachine

import "golang.org/x/crypto/ssh"

// SetDialForTest swaps the package-level ssh.Dial used by WaitForSSH for the
// duration of a test, returning a restore func.
func SetDialForTest(stub func(network, addr string, config *ssh.ClientConfig) (*ssh.Client, error)) func() {
	original := dial
	dial = stub

	return func() { dial = original }
}
