// BEGIN OPENVIDU BLOCK
package service

import (
	"net"
	"testing"
	"time"

	"github.com/jxskiss/base62"
	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"

	"github.com/livekit/livekit-server/pkg/config"
	"github.com/livekit/mediatransportutil/pkg/rtcconfig"
)

func TestResolveTURNRelayAddress(t *testing.T) {
	t.Run("explicit RelayAddress is always used", func(t *testing.T) {
		conf := &config.Config{}
		conf.TURN.RelayAddress = "10.0.0.99"
		conf.RTC.NodeIP = "203.0.113.1"
		conf.PubliclyReachable = true

		addr, err := resolveTURNRelayAddress(conf)
		require.NoError(t, err)
		require.Equal(t, "10.0.0.99", addr)
	})

	t.Run("explicit RelayAddress used even when not publicly reachable", func(t *testing.T) {
		conf := &config.Config{}
		conf.TURN.RelayAddress = "10.0.0.99"
		conf.RTC.NodeIP = "192.168.1.1"
		conf.PubliclyReachable = false

		addr, err := resolveTURNRelayAddress(conf)
		require.NoError(t, err)
		require.Equal(t, "10.0.0.99", addr)
	})

	t.Run("publicly reachable with no RelayAddress falls back to NodeIP", func(t *testing.T) {
		conf := &config.Config{}
		conf.RTC.NodeIP = "203.0.113.1"
		conf.PubliclyReachable = true

		addr, err := resolveTURNRelayAddress(conf)
		require.NoError(t, err)
		require.Equal(t, "203.0.113.1", addr)
	})

	t.Run("not publicly reachable with no RelayAddress uses first local IP", func(t *testing.T) {
		localIPs, err := rtcconfig.GetLocalIPAddresses(false, nil)
		if err != nil {
			t.Skipf("could not get local IP addresses: %v", err)
		}
		if len(localIPs) == 0 {
			t.Skip("no local IP addresses found")
		}

		conf := &config.Config{}
		conf.RTC.NodeIP = "203.0.113.1"
		conf.PubliclyReachable = false

		addr, err := resolveTURNRelayAddress(conf)
		require.NoError(t, err)
		require.Equal(t, localIPs[0], addr)
	})
}

// ---------------------------------------------------------------------------
// TURNAuthHandler — CreateUsername / ParseUsername
// ---------------------------------------------------------------------------

func newTestAuthHandler() *TURNAuthHandler {
	return NewTURNAuthHandler(auth.NewSimpleKeyProvider("testkey", "testsecret"))
}

// newTestAuthHandlerAt returns a handler whose clock is pinned to the given time,
// letting expiry tests run without time.Sleep.
func newTestAuthHandlerAt(now time.Time) *TURNAuthHandler {
	h := newTestAuthHandler()
	h.now = func() time.Time { return now }
	return h
}

func TestTURNAuthHandler_CreateParseUsername_Roundtrip(t *testing.T) {
	h := newTestAuthHandler()

	username := h.CreateUsername("testkey", "PA_participant1", 0)
	apiKey, pID, expiry, err := h.ParseUsername(username)
	require.NoError(t, err)
	require.Equal(t, "testkey", apiKey)
	require.Equal(t, livekit.ParticipantID("PA_participant1"), pID)
	require.True(t, expiry.IsZero(), "zero TTL should produce legacy no-expiry username")
}

func TestTURNAuthHandler_CreateParseUsername_DifferentInputs(t *testing.T) {
	h := newTestAuthHandler()

	tests := []struct {
		apiKey string
		pID    livekit.ParticipantID
	}{
		{"key1", "PA_abc"},
		{"key2", "PA_xyz123"},
		{"k", "PA_p"},
		{"longapikey12345", "PA_longparticipantid67890"},
	}
	for _, tc := range tests {
		username := h.CreateUsername(tc.apiKey, tc.pID, 0)
		gotKey, gotPID, expiry, err := h.ParseUsername(username)
		require.NoError(t, err)
		require.Equal(t, tc.apiKey, gotKey)
		require.Equal(t, tc.pID, gotPID)
		require.True(t, expiry.IsZero())
	}
}

func TestTURNAuthHandler_CreateParseUsername_WithTTL_Roundtrip(t *testing.T) {
	fixedNow := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(fixedNow)

	username := h.CreateUsername("testkey", "PA_p1", time.Hour)
	apiKey, pID, expiry, err := h.ParseUsername(username)
	require.NoError(t, err)
	require.Equal(t, "testkey", apiKey)
	require.Equal(t, livekit.ParticipantID("PA_p1"), pID)
	require.False(t, expiry.IsZero())
	require.Equal(t, fixedNow.Add(time.Hour).Unix(), expiry.Unix())
}

func TestTURNAuthHandler_ParseUsername_InvalidBase62(t *testing.T) {
	h := newTestAuthHandler()

	_, _, _, err := h.ParseUsername("!!!not-base62!!!")
	require.Error(t, err)
}

func TestTURNAuthHandler_ParseUsername_MissingSeparator(t *testing.T) {
	h := newTestAuthHandler()

	// base62-encode "noseparator" — no pipe, Split produces 1 part, falls to default → error.
	encoded := base62.EncodeToString([]byte("noseparator"))
	_, _, _, err := h.ParseUsername(encoded)
	require.Error(t, err)
}

func TestTURNAuthHandler_ParseUsername_TooManyPipes(t *testing.T) {
	h := newTestAuthHandler()

	// base62-encode "a|b|c|d" — 4 parts, neither legacy (2) nor expiry-carrying (3).
	encoded := base62.EncodeToString([]byte("a|b|c|d"))
	_, _, _, err := h.ParseUsername(encoded)
	require.Error(t, err)
}

func TestTURNAuthHandler_ParseUsername_ThreePartForm_BadExpiry(t *testing.T) {
	h := newTestAuthHandler()

	// 3-part form is valid only when the third field parses as int64 unix seconds.
	encoded := base62.EncodeToString([]byte("testkey|PA_p1|notanumber"))
	_, _, _, err := h.ParseUsername(encoded)
	require.Error(t, err)
}

func TestTURNAuthHandler_ParseUsername_Empty(t *testing.T) {
	h := newTestAuthHandler()
	_, _, _, err := h.ParseUsername("")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// TURNAuthHandler — CreatePassword
// ---------------------------------------------------------------------------

func TestTURNAuthHandler_CreatePassword_ValidKey(t *testing.T) {
	h := newTestAuthHandler()

	pw, err := h.CreatePassword("testkey", "PA_participant1")
	require.NoError(t, err)
	require.NotEmpty(t, pw)
}

func TestTURNAuthHandler_CreatePassword_Deterministic(t *testing.T) {
	h := newTestAuthHandler()

	pw1, err := h.CreatePassword("testkey", "PA_p1")
	require.NoError(t, err)
	pw2, err := h.CreatePassword("testkey", "PA_p1")
	require.NoError(t, err)
	require.Equal(t, pw1, pw2)
}

func TestTURNAuthHandler_CreatePassword_DifferentParticipants(t *testing.T) {
	h := newTestAuthHandler()

	pw1, err := h.CreatePassword("testkey", "PA_p1")
	require.NoError(t, err)
	pw2, err := h.CreatePassword("testkey", "PA_p2")
	require.NoError(t, err)
	require.NotEqual(t, pw1, pw2)
}

func TestTURNAuthHandler_CreatePassword_InvalidKey(t *testing.T) {
	h := newTestAuthHandler()

	_, err := h.CreatePassword("unknownkey", "PA_p1")
	require.ErrorIs(t, err, ErrInvalidAPIKey)
}

// ---------------------------------------------------------------------------
// TURNAuthHandler — HandleAuth
// ---------------------------------------------------------------------------

func TestTURNAuthHandler_HandleAuth_ValidCredentials(t *testing.T) {
	h := newTestAuthHandler()

	username := h.CreateUsername("testkey", "PA_p1", 0)
	key, ok := h.HandleAuth(username, LivekitRealm, nil)
	require.True(t, ok)
	require.NotNil(t, key)
	require.NotEmpty(t, key)
}

func TestTURNAuthHandler_HandleAuth_InvalidBase62Username(t *testing.T) {
	h := newTestAuthHandler()

	_, ok := h.HandleAuth("!!!invalid!!!", LivekitRealm, nil)
	require.False(t, ok)
}

func TestTURNAuthHandler_HandleAuth_MalformedUsername(t *testing.T) {
	h := newTestAuthHandler()

	// base62-encode a string without the pipe separator.
	encoded := base62.EncodeToString([]byte("nopipe"))
	_, ok := h.HandleAuth(encoded, LivekitRealm, nil)
	require.False(t, ok)
}

func TestTURNAuthHandler_HandleAuth_UnknownAPIKey(t *testing.T) {
	h := newTestAuthHandler()

	// Valid format but the API key is not known to the provider.
	username := h.CreateUsername("unknownkey", "PA_p1", 0)
	_, ok := h.HandleAuth(username, LivekitRealm, nil)
	require.False(t, ok)
}

func TestTURNAuthHandler_HandleAuth_DifferentParticipantsGetDifferentKeys(t *testing.T) {
	h := newTestAuthHandler()

	u1 := h.CreateUsername("testkey", "PA_p1", 0)
	u2 := h.CreateUsername("testkey", "PA_p2", 0)

	key1, ok1 := h.HandleAuth(u1, LivekitRealm, nil)
	key2, ok2 := h.HandleAuth(u2, LivekitRealm, nil)
	require.True(t, ok1)
	require.True(t, ok2)
	require.NotEqual(t, key1, key2)
}

// ---------------------------------------------------------------------------
// TURNAuthHandler — HandleAuth with TTL / expiry
// ---------------------------------------------------------------------------

func TestTURNAuthHandler_HandleAuth_ValidFresh(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)
	username := h.CreateUsername("testkey", "PA_p1", time.Hour)

	// Fast-forward clock just past issuance: well before expiry.
	h.now = func() time.Time { return issuedAt.Add(5 * time.Minute) }

	_, ok := h.HandleAuth(username, LivekitRealm, nil)
	require.True(t, ok)
}

func TestTURNAuthHandler_HandleAuth_ValidAtBoundary(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)
	username := h.CreateUsername("testkey", "PA_p1", time.Hour)

	// Exactly at expiry — strict After check means still valid.
	h.now = func() time.Time { return issuedAt.Add(time.Hour) }

	_, ok := h.HandleAuth(username, LivekitRealm, nil)
	require.True(t, ok)
}

func TestTURNAuthHandler_HandleAuth_ValidWithinSkew(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)
	username := h.CreateUsername("testkey", "PA_p1", time.Hour)

	// 2m past expiry — inside the 5m skew tolerance.
	h.now = func() time.Time { return issuedAt.Add(time.Hour + 2*time.Minute) }

	_, ok := h.HandleAuth(username, LivekitRealm, nil)
	require.True(t, ok)
}

func TestTURNAuthHandler_HandleAuth_ExpiredBeyondSkew(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)
	username := h.CreateUsername("testkey", "PA_p1", time.Hour)

	// 5m + 1s past expiry — beyond skew, must reject.
	h.now = func() time.Time { return issuedAt.Add(time.Hour + 5*time.Minute + time.Second) }

	_, ok := h.HandleAuth(username, LivekitRealm, nil)
	require.False(t, ok)
}

func TestTURNAuthHandler_HandleAuth_LegacyUsernameNeverExpires(t *testing.T) {
	h := newTestAuthHandler()
	username := h.CreateUsername("testkey", "PA_p1", 0) // legacy, no expiry

	// Advance clock arbitrarily far; legacy usernames must still authenticate.
	h.now = func() time.Time { return time.Unix(9_999_999_999, 0) }

	_, ok := h.HandleAuth(username, LivekitRealm, nil)
	require.True(t, ok)
}

func TestTURNAuthHandler_HandleAuth_MalformedExpiry(t *testing.T) {
	h := newTestAuthHandler()

	// 3-part username with a non-numeric expiry — ParseUsername must reject
	// before HandleAuth reaches the hash step.
	encoded := base62.EncodeToString([]byte("testkey|PA_p1|notanumber"))
	_, ok := h.HandleAuth(encoded, LivekitRealm, nil)
	require.False(t, ok)
}

// ---------------------------------------------------------------------------
// extractPort — all branches
// ---------------------------------------------------------------------------

// stringAddr implements net.Addr with a custom string representation,
// exercising the fallback branch in extractPort.
type stringAddr struct {
	network string
	addr    string
}

func (a stringAddr) Network() string { return a.network }
func (a stringAddr) String() string  { return a.addr }

func TestExtractPort_UDPAddr(t *testing.T) {
	port, err := extractPort(&net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 8080})
	require.NoError(t, err)
	require.Equal(t, uint16(8080), port)
}

func TestExtractPort_TCPAddr(t *testing.T) {
	port, err := extractPort(&net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 443})
	require.NoError(t, err)
	require.Equal(t, uint16(443), port)
}

func TestExtractPort_FallbackAddr(t *testing.T) {
	port, err := extractPort(stringAddr{"custom", "10.0.0.1:5500"})
	require.NoError(t, err)
	require.Equal(t, uint16(5500), port)
}

func TestExtractPort_FallbackAddr_InvalidFormat(t *testing.T) {
	_, err := extractPort(stringAddr{"custom", "not-a-host-port"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot extract port")
}

func TestExtractPort_FallbackAddr_InvalidPort(t *testing.T) {
	_, err := extractPort(stringAddr{"custom", "10.0.0.1:abc"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot parse port")
}

// END OPENVIDU BLOCK
