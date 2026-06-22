// BEGIN OPENVIDU BLOCK
//
// Integration tests for the embedded TURN server. Unlike the unit tests in
// turn_security_test.go (which call the permission handler directly), these boot
// a real server via NewTurnServer on a loopback UDP port and drive it with a
// pion TURN client, asserting the actual on-the-wire behavior of:
//   - the TURNSecurity wiring (open-relay regression): only local/cluster IPs
//     may be reached, never arbitrary public IPs;
//   - authentication (TURNAuthHandler): valid/invalid/expired/malformed creds;
//   - enable_rfc6062: TCP allocations rejected by default, allowed when enabled.
package service

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/jxskiss/base62"
	"github.com/pion/logging"
	"github.com/pion/turn/v5"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/livekit/mediatransportutil/pkg/rtcconfig"
	"github.com/livekit/protocol/livekit"

	"github.com/livekit/livekit-server/pkg/config"
)

// turnTestAPIKey / turnTestAPISecret and newTestTurnAuthHandler() are defined in
// turn_test.go (same package) and reused here.
const turnTestPID = livekit.ParticipantID("PA_test")

// ---------------------------------------------------------------------------
// Helpers: real server + pion client
// ---------------------------------------------------------------------------

// freeUDPPort returns an ephemeral UDP port on the loopback interface.
func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer c.Close() //nolint:errcheck
	return c.LocalAddr().(*net.UDPAddr).Port
}

// newTestTurnServerConfig returns a minimal, valid config for an embedded
// UDP-only TURN server bound to loopback.
func newTestTurnServerConfig(udpPort int) *config.Config {
	conf := &config.Config{}
	conf.TURN.Enabled = true
	conf.TURN.UDPPort = udpPort
	conf.TURN.BindAddresses = []string{"127.0.0.1"}
	conf.TURN.RelayPortRangeStart = 40000
	conf.TURN.RelayPortRangeEnd = 50000
	conf.TURN.RelayAddress = "127.0.0.1" // deterministic; skips external-IP detection
	conf.RTC.NodeIP = rtcconfig.NodeIP{V4: "127.0.0.1"}
	conf.RTC.ICEPortRangeStart = 50000
	conf.RTC.ICEPortRangeEnd = 60000
	return conf
}

// startTestTurnServer boots a TURN server with the given config and auth handler
// and registers its cleanup. rc may be nil for static (no-Redis) mode.
func startTestTurnServer(t *testing.T, conf *config.Config, h *TURNAuthHandler, rc redis.UniversalClient) {
	t.Helper()
	server, err := NewTurnServer(conf, getTURNAuthHandlerFunc(h), false, rc)
	require.NoError(t, err)
	require.NotNil(t, server)
	t.Cleanup(func() { _ = server.Close() })
}

// dialTURNClient builds a pion TURN client that presents exactly the given
// username/password (so we can drive arbitrary, including invalid, credentials).
func dialTURNClient(t *testing.T, udpPort int, username, password string) *turn.Client {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "0.0.0.0:0")
	require.NoError(t, err)

	logf := logging.NewDefaultLoggerFactory()
	logf.DefaultLogLevel = logging.LogLevelError

	client, err := turn.NewClient(&turn.ClientConfig{
		STUNServerAddr: fmt.Sprintf("127.0.0.1:%d", udpPort),
		TURNServerAddr: fmt.Sprintf("127.0.0.1:%d", udpPort),
		Username:       username,
		Password:       password,
		Realm:          LivekitRealm,
		Conn:           conn,
		LoggerFactory:  logf,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		client.Close()
		_ = conn.Close()
	})
	require.NoError(t, client.Listen())
	return client
}

// dialValidTURNClient connects with valid participant-grade credentials minted
// by the production TURNAuthHandler.
func dialValidTURNClient(t *testing.T, udpPort int, h *TURNAuthHandler) *turn.Client {
	t.Helper()
	username, expiry := h.CreateUsername(turnTestAPIKey, turnTestPID, 3600)
	password, err := h.CreatePassword(turnTestAPIKey, turnTestPID, expiry)
	require.NoError(t, err)
	return dialTURNClient(t, udpPort, username, password)
}

// ---------------------------------------------------------------------------
// TURNSecurity wiring (open-relay regression)
// ---------------------------------------------------------------------------

// TestNewTurnServer_EnforcesTURNSecurity is the regression test for the
// open-relay defect: NewTurnServer must wire TURNSecurity as the relay
// PermissionHandler, so that an authenticated client can only create
// permissions for local/cluster-node IPs — never for arbitrary public IPs.
//
// Before the fix the inline handler returned true for every public IP, turning
// the relay into an open proxy.
func TestNewTurnServer_EnforcesTURNSecurity(t *testing.T) {
	const clusterIP = "10.9.8.7" // registered cluster-node IP (allowed)

	mr, rc := newMiniredis(t)
	setNode(t, mr, "node-1", clusterIP, clusterIP)

	udpPort := freeUDPPort(t)
	h := newTestTurnAuthHandler()
	startTestTurnServer(t, newTestTurnServerConfig(udpPort), h, rc)

	client := dialValidTURNClient(t, udpPort, h)

	// Allocation proves the credentials are valid (we are an authenticated client).
	relayConn, err := client.Allocate()
	require.NoError(t, err, "valid participant credentials should allocate a relay")
	t.Cleanup(func() { _ = relayConn.Close() })

	// A registered cluster-node IP must be permitted (the allowlist allows it).
	require.NoError(t,
		client.CreatePermission(&net.UDPAddr{IP: net.ParseIP(clusterIP), Port: 55000}),
		"registered cluster-node IP must be permitted")

	// Arbitrary public IPs must be denied — the relay must NOT be an open proxy.
	for _, ip := range []string{"1.1.1.1", "8.8.8.8", "192.0.2.1"} {
		require.Error(t,
			client.CreatePermission(&net.UDPAddr{IP: net.ParseIP(ip), Port: 55000}),
			"public IP %s must be denied (open-relay regression)", ip)
	}

	// An unregistered private IP must also be denied.
	require.Error(t,
		client.CreatePermission(&net.UDPAddr{IP: net.ParseIP("10.5.0.250"), Port: 55000}),
		"unregistered private IP must be denied")
}

// TestNewTurnServer_NoRedis_StaticAllowlist verifies the no-Redis (dev) path:
// TURNSecurity falls back to the configured NodeIP / ResolvedRelayAddress, and
// public IPs are still denied.
func TestNewTurnServer_NoRedis_StaticAllowlist(t *testing.T) {
	udpPort := freeUDPPort(t)
	h := newTestTurnAuthHandler()
	startTestTurnServer(t, newTestTurnServerConfig(udpPort), h, nil) // rc == nil

	client := dialValidTURNClient(t, udpPort, h)

	relayConn, err := client.Allocate()
	require.NoError(t, err)
	t.Cleanup(func() { _ = relayConn.Close() })

	// ResolvedRelayAddress (127.0.0.1 here) is in the static allow set.
	require.NoError(t,
		client.CreatePermission(&net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 55000}),
		"configured relay address must be permitted in static mode")

	// Public IP still denied.
	require.Error(t,
		client.CreatePermission(&net.UDPAddr{IP: net.ParseIP("1.1.1.1"), Port: 55000}),
		"public IP must be denied in static mode")
}

// ---------------------------------------------------------------------------
// enable_rfc6062 wiring
// ---------------------------------------------------------------------------

// TestNewTurnServer_RFC6062_DisabledByDefault_RejectsTCPAllocate verifies that
// enable_rfc6062 is wired through NewTurnServer: by default a TCP (RFC 6062)
// allocation is rejected end-to-end.
func TestNewTurnServer_RFC6062_DisabledByDefault_RejectsTCPAllocate(t *testing.T) {
	udpPort := freeUDPPort(t)
	h := newTestTurnAuthHandler()
	startTestTurnServer(t, newTestTurnServerConfig(udpPort), h, nil) // EnableRFC6062 defaults to false

	client := dialValidTURNClient(t, udpPort, h)
	_, err := client.AllocateTCP()
	require.Error(t, err, "RFC 6062 disabled by default => TCP allocation must be rejected")
}

// TestNewTurnServer_RFC6062_Enabled_AllowsTCPAllocate verifies the opposite: when
// enable_rfc6062 is true, a TCP allocation succeeds end-to-end.
func TestNewTurnServer_RFC6062_Enabled_AllowsTCPAllocate(t *testing.T) {
	udpPort := freeUDPPort(t)
	conf := newTestTurnServerConfig(udpPort)
	conf.TURN.EnableRFC6062 = true

	h := newTestTurnAuthHandler()
	startTestTurnServer(t, conf, h, nil)

	client := dialValidTURNClient(t, udpPort, h)
	alloc, err := client.AllocateTCP()
	require.NoError(t, err, "RFC 6062 enabled => TCP allocation must succeed")
	if alloc != nil {
		_ = alloc.Close()
	}
}

// ---------------------------------------------------------------------------
// Authentication (TURNAuthHandler)
// ---------------------------------------------------------------------------

// startAuthTestTurnServer boots a static-mode (no-Redis) server and returns its
// port and the auth handler used to mint credentials.
func startAuthTestTurnServer(t *testing.T) (int, *TURNAuthHandler) {
	t.Helper()
	udpPort := freeUDPPort(t)
	h := newTestTurnAuthHandler()
	startTestTurnServer(t, newTestTurnServerConfig(udpPort), h, nil)
	return udpPort, h
}

// Credentials minted by the handler are accepted by the same handler at the
// server (round-trip).
func TestTURNAuth_ValidCredentialsAllocate(t *testing.T) {
	udpPort, h := startAuthTestTurnServer(t)

	username, expiry := h.CreateUsername(turnTestAPIKey, turnTestPID, 3600)
	password, err := h.CreatePassword(turnTestAPIKey, turnTestPID, expiry)
	require.NoError(t, err)

	client := dialTURNClient(t, udpPort, username, password)
	relay, err := client.Allocate()
	require.NoError(t, err, "valid credentials must allocate")
	_ = relay.Close()
}

// A valid username with a bad password fails the MESSAGE-INTEGRITY check.
func TestTURNAuth_WrongPasswordRejected(t *testing.T) {
	udpPort, h := startAuthTestTurnServer(t)

	username, _ := h.CreateUsername(turnTestAPIKey, turnTestPID, 3600)

	client := dialTURNClient(t, udpPort, username, "this-is-not-the-right-password")
	_, err := client.Allocate()
	require.Error(t, err, "wrong password must be rejected")
}

// A username encoding an API key the server doesn't know is rejected
// (GetSecret returns "" -> ErrInvalidAPIKey).
func TestTURNAuth_UnknownAPIKeyRejected(t *testing.T) {
	udpPort, h := startAuthTestTurnServer(t)

	username, _ := h.CreateUsername("some-other-key", turnTestPID, 3600)

	client := dialTURNClient(t, udpPort, username, "irrelevant")
	_, err := client.Allocate()
	require.Error(t, err, "unknown API key must be rejected")
}

// An already-expired credential cannot allocate. The password is computed
// directly (CreatePassword refuses to mint a password for an expired credential).
func TestTURNAuth_ExpiredCredentialRejectedOnAllocate(t *testing.T) {
	udpPort, h := startAuthTestTurnServer(t)

	username, expiry := h.CreateUsername(turnTestAPIKey, turnTestPID, -3600) // expiry in the past
	password, err := h.computePassword(turnTestAPIKey, turnTestPID, expiry)
	require.NoError(t, err)

	client := dialTURNClient(t, udpPort, username, password)
	_, err = client.Allocate()
	require.Error(t, err, "expired credential must not allocate")
}

// Usernames that aren't valid base62 or don't decode to 2/3 pipe-separated parts
// are rejected.
func TestTURNAuth_MalformedUsernameRejected(t *testing.T) {
	udpPort, _ := startAuthTestTurnServer(t)

	cases := map[string]string{
		"not base62":  "!!! not base62 !!!",
		"single part": base62.EncodeToString([]byte("only-one-part")),
		"four parts":  base62.EncodeToString([]byte("a|b|c|d")),
		"empty":       "",
	}
	for name, username := range cases {
		t.Run(name, func(t *testing.T) {
			client := dialTURNClient(t, udpPort, username, "whatever")
			_, err := client.Allocate()
			require.Error(t, err, "malformed username (%s) must be rejected", name)
		})
	}
}

// A 2-part username (no expiry) is accepted, with the password computed for
// expiry==0.
func TestTURNAuth_CredentialWithoutExpiryAllocate(t *testing.T) {
	udpPort, h := startAuthTestTurnServer(t)

	username := base62.EncodeToString([]byte(turnTestAPIKey + "|" + string(turnTestPID)))
	password, err := h.computePassword(turnTestAPIKey, turnTestPID, 0)
	require.NoError(t, err)

	client := dialTURNClient(t, udpPort, username, password)
	relay, err := client.Allocate()
	require.NoError(t, err, "credential without expiry must allocate")
	_ = relay.Close()
}

// The TTL is enforced only on the initial Allocate: a long-running session can
// keep issuing CreatePermission/Refresh past the credential expiry — but a
// brand-new Allocate with the expired credential is rejected.
func TestTURNAuth_ExpiredCredentialStillAllowsRefreshButNotNewAllocate(t *testing.T) {
	udpPort, h := startAuthTestTurnServer(t)

	const ttlSeconds = 2
	username, expiry := h.CreateUsername(turnTestAPIKey, turnTestPID, ttlSeconds)
	password, err := h.CreatePassword(turnTestAPIKey, turnTestPID, expiry)
	require.NoError(t, err)

	// Allocate while the credential is still valid.
	client := dialTURNClient(t, udpPort, username, password)
	relay, err := client.Allocate()
	require.NoError(t, err, "credential should be valid at allocate time")
	t.Cleanup(func() { _ = relay.Close() })

	// Let the credential expire.
	time.Sleep((ttlSeconds + 1) * time.Second)

	// A non-Allocate request (CreatePermission) on the existing allocation still
	// authenticates — the TTL is skipped for it. 127.0.0.1 is the configured
	// relay address, allowed by TURNSecurity in static mode.
	require.NoError(t,
		client.CreatePermission(&net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 55000}),
		"expired credential must still authorize CreatePermission on an existing allocation")

	// But a brand-new Allocate with the same (now expired) credential is rejected.
	client2 := dialTURNClient(t, udpPort, username, password)
	_, err = client2.Allocate()
	require.Error(t, err, "expired credential must not allocate a new relay")
}

// END OPENVIDU BLOCK
