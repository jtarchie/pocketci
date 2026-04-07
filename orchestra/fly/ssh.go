package fly

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/ssh"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"

	fly "github.com/superfly/fly-go"
)

// flyTunnel wraps a userspace WireGuard tunnel to the Fly 6PN network.
// It allows dialing machines by their private IPv6 addresses.
type flyTunnel struct {
	dev      *device.Device
	tnet     *netstack.Net
	peerName string
	orgID    string
}

// createTunnel establishes a userspace WireGuard tunnel to the Fly 6PN network.
// It creates a WireGuard peer via the Fly API and configures a local tunnel device.
func (f *Fly) createTunnel(ctx context.Context) (*flyTunnel, error) {
	org, err := f.apiClient.GetOrganizationBySlug(ctx, f.org)
	if err != nil {
		return nil, fmt.Errorf("failed to get organization %q: %w", f.org, err)
	}

	// Generate WireGuard Curve25519 keypair
	var privKey [32]byte
	_, randErr := rand.Read(privKey[:])
	if randErr != nil {
		return nil, fmt.Errorf("failed to generate WireGuard key: %w", randErr)
	}

	// Clamp private key per Curve25519 requirements
	privKey[0] &= 248
	privKey[31] = (privKey[31] & 127) | 64

	pubKey, err := curve25519.X25519(privKey[:], curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("failed to derive WireGuard public key: %w", err)
	}

	// Peer name must be DNS-compatible: lowercase, letters/digits/hyphens only
	peerName := SanitizeAppName("pocketci-cache-" + f.namespace)

	// Create WireGuard peer via Fly API
	peer, err := f.apiClient.CreateWireGuardPeer(
		ctx, org.ID, f.region, peerName,
		base64.StdEncoding.EncodeToString(pubKey), "",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create WireGuard peer: %w", err)
	}

	// Parse our assigned 6PN address.
	// The API may return with CIDR ("fdaa:0:18:a7b:d6b:0:a:2/120") or
	// without ("fdaa:1:528b:a7b:8cfe:0:a:102"). Handle both cases.
	var localAddr netip.Addr

	if strings.Contains(peer.Peerip, "/") {
		prefix, err := netip.ParsePrefix(peer.Peerip)
		if err != nil {
			_ = f.apiClient.RemoveWireGuardPeer(ctx, org.ID, peerName)
			return nil, fmt.Errorf("failed to parse peer IP prefix %q: %w", peer.Peerip, err)
		}

		localAddr = prefix.Addr()
	} else {
		var err error

		localAddr, err = netip.ParseAddr(peer.Peerip)
		if err != nil {
			_ = f.apiClient.RemoveWireGuardPeer(ctx, org.ID, peerName)
			return nil, fmt.Errorf("failed to parse peer IP %q: %w", peer.Peerip, err)
		}
	}

	// DNS server on 6PN network
	dnsAddr := netip.MustParseAddr("fdaa::3")

	// Create userspace TUN device with netstack
	tunDev, tnet, err := netstack.CreateNetTUN(
		[]netip.Addr{localAddr},
		[]netip.Addr{dnsAddr},
		1280, // MTU
	)
	if err != nil {
		_ = f.apiClient.RemoveWireGuardPeer(ctx, org.ID, peerName)
		return nil, fmt.Errorf("failed to create netstack TUN: %w", err)
	}

	// Decode server's WireGuard public key
	serverPubKeyBytes, err := base64.StdEncoding.DecodeString(peer.Pubkey)
	if err != nil {
		_ = f.apiClient.RemoveWireGuardPeer(ctx, org.ID, peerName)
		return nil, fmt.Errorf("failed to decode server public key: %w", err)
	}

	// Resolve the endpoint hostname to an IP address.
	// WireGuard's IPC config requires a numeric IP via netip.ParseAddrPort.
	endpointHost := peer.Endpointip

	endpointIPs, err := net.DefaultResolver.LookupHost(ctx, endpointHost)
	if err != nil || len(endpointIPs) == 0 {
		_ = f.apiClient.RemoveWireGuardPeer(ctx, org.ID, peerName)
		return nil, fmt.Errorf("failed to resolve WireGuard endpoint %q: %w", endpointHost, err)
	}

	// Format endpoint as netip.AddrPort for the WireGuard IPC config
	resolvedAddr, err := netip.ParseAddr(endpointIPs[0])
	if err != nil {
		_ = f.apiClient.RemoveWireGuardPeer(ctx, org.ID, peerName)
		return nil, fmt.Errorf("failed to parse resolved endpoint address %q: %w", endpointIPs[0], err)
	}

	endpointAddrPort := netip.AddrPortFrom(resolvedAddr, 51820)

	// Configure WireGuard device via IPC
	ipcConfig := fmt.Sprintf(`private_key=%s
public_key=%s
endpoint=%s
allowed_ip=fdaa::/16
persistent_keepalive_interval=15`,
		hex.EncodeToString(privKey[:]),
		hex.EncodeToString(serverPubKeyBytes),
		endpointAddrPort.String(),
	)

	dev := device.NewDevice(tunDev, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))

	ipcErr := dev.IpcSet(ipcConfig)
	if ipcErr != nil {
		dev.Close()
		_ = f.apiClient.RemoveWireGuardPeer(ctx, org.ID, peerName)
		return nil, fmt.Errorf("failed to configure WireGuard device: %w", ipcErr)
	}

	upErr := dev.Up()
	if upErr != nil {
		dev.Close()
		_ = f.apiClient.RemoveWireGuardPeer(ctx, org.ID, peerName)
		return nil, fmt.Errorf("failed to bring up WireGuard device: %w", upErr)
	}

	return &flyTunnel{
		dev:      dev,
		tnet:     tnet,
		peerName: peerName,
		orgID:    org.ID,
	}, nil
}

// close tears down the WireGuard tunnel and removes the peer from Fly.
func (t *flyTunnel) close(ctx context.Context, apiClient *fly.Client) {
	t.dev.Close()
	_ = apiClient.RemoveWireGuardPeer(ctx, t.orgID, t.peerName)
}

// dialSSH connects to a Fly machine via SSH through the WireGuard tunnel.
// It uses an SSH certificate issued by the Fly API for authentication.
func (f *Fly) dialSSH(ctx context.Context, tunnel *flyTunnel, machineIP string) (*ssh.Client, error) {
	// Generate ed25519 keypair for SSH
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate SSH key: %w", err)
	}

	// Issue SSH certificate via Fly API
	cert, err := f.apiClient.IssueSSHCertificate(ctx, tunnel.orgID, []string{"root"}, []string{f.appName}, nil, pubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to issue SSH certificate: %w", err)
	}

	// Parse the private key into an ssh.Signer
	signer, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create SSH signer: %w", err)
	}

	// Parse the issued SSH certificate
	parsedPubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(cert.Certificate))
	if err != nil {
		return nil, fmt.Errorf("failed to parse SSH certificate: %w", err)
	}

	sshCert, ok := parsedPubKey.(*ssh.Certificate)
	if !ok {
		return nil, errors.New("returned SSH key is not a certificate")
	}

	// Create a certificate signer combining the cert and private key
	certSigner, err := ssh.NewCertSigner(sshCert, signer)
	if err != nil {
		return nil, fmt.Errorf("failed to create certificate signer: %w", err)
	}

	sshConfig := &ssh.ClientConfig{
		User:            "root",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(certSigner)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // Fly machines don't have stable host keys
	}

	// Dial through the WireGuard tunnel to the machine's 6PN address
	addr := net.JoinHostPort(machineIP, "22")

	tcpConn, err := tunnel.tnet.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to dial %s through WireGuard tunnel: %w", addr, err)
	}

	// Perform SSH handshake over the WireGuard connection
	sshConn, chans, reqs, err := ssh.NewClientConn(tcpConn, addr, sshConfig)
	if err != nil {
		_ = tcpConn.Close()
		return nil, fmt.Errorf("SSH handshake failed: %w", err)
	}

	return ssh.NewClient(sshConn, chans, reqs), nil
}
