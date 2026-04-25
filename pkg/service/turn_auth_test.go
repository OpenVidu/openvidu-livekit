// BEGIN OPENVIDU BLOCK
package service

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/jxskiss/base62"
	"github.com/pion/turn/v4"
	"github.com/stretchr/testify/require"

	"github.com/livekit/mediatransportutil/pkg/rtcconfig"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"

	"github.com/livekit/livekit-server/pkg/config"
)

// ===========================================================================
// TURNAuthHandler — credential format, parsing, password derivation, and
// authentication. Tests progress from primitives (username encoding, password
// derivation) up through composition (CreateCredentials) and finish with
// threat scenarios that exercise the long-term-credential scheme end-to-end.
// A failure in the tampering/input-validation sections indicates an
// authentication bypass.
// ===========================================================================

// ---------------------------------------------------------------------------
// Helpers
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

// tamperedAuthRejected returns true when a captured (username, password)
// pair cannot be replayed against the server with a tampered username.
// Either:
//   - HandleAuth rejects the tampered username outright, OR
//   - HandleAuth accepts it at the parse layer but the server-derived key
//     differs from the replayed key (MESSAGE-INTEGRITY fails in pion/turn).
func tamperedAuthRejected(h *TURNAuthHandler, tamperedUsername string, capturedPassword string) bool {
	serverKey, ok := h.HandleAuth(tamperedUsername, LivekitRealm, nil)
	if !ok {
		return true // rejected at parse / expiry / key-lookup layer
	}
	replayedKey := turn.GenerateAuthKey(tamperedUsername, LivekitRealm, capturedPassword)
	return string(replayedKey) != string(serverKey)
}

func encodeUsername(parts ...string) string {
	var joined string
	for i, p := range parts {
		if i > 0 {
			joined += "|"
		}
		joined += p
	}
	return base62.EncodeToString([]byte(joined))
}

// ---------------------------------------------------------------------------
// Username encoding (CreateUsername / ParseUsername)
// ---------------------------------------------------------------------------

func TestTURNAuthHandler_CreateParseUsername_Roundtrip(t *testing.T) {
	h := newTestAuthHandler()

	username := h.CreateUsername("testkey", "PA_participant1", time.Time{})
	apiKey, pID, expiry, err := h.ParseUsername(username)
	require.NoError(t, err)
	require.Equal(t, "testkey", apiKey)
	require.Equal(t, livekit.ParticipantID("PA_participant1"), pID)
	require.True(t, expiry.IsZero(), "zero expiry should produce legacy no-expiry username")
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
		username := h.CreateUsername(tc.apiKey, tc.pID, time.Time{})
		gotKey, gotPID, expiry, err := h.ParseUsername(username)
		require.NoError(t, err)
		require.Equal(t, tc.apiKey, gotKey)
		require.Equal(t, tc.pID, gotPID)
		require.True(t, expiry.IsZero())
	}
}

func TestTURNAuthHandler_CreateParseUsername_WithExpiry_Roundtrip(t *testing.T) {
	h := newTestAuthHandler()
	want := time.Unix(1_700_003_600, 0)

	username := h.CreateUsername("testkey", "PA_p1", want)
	apiKey, pID, expiry, err := h.ParseUsername(username)
	require.NoError(t, err)
	require.Equal(t, "testkey", apiKey)
	require.Equal(t, livekit.ParticipantID("PA_p1"), pID)
	require.False(t, expiry.IsZero())
	require.Equal(t, want.Unix(), expiry.Unix())
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
// Password derivation (CreatePassword)
// ---------------------------------------------------------------------------

func TestTURNAuthHandler_CreatePassword_ValidKey(t *testing.T) {
	h := newTestAuthHandler()

	pw, err := h.CreatePassword("testkey", "PA_participant1", time.Time{})
	require.NoError(t, err)
	require.NotEmpty(t, pw)
}

func TestTURNAuthHandler_CreatePassword_Deterministic(t *testing.T) {
	h := newTestAuthHandler()

	pw1, err := h.CreatePassword("testkey", "PA_p1", time.Time{})
	require.NoError(t, err)
	pw2, err := h.CreatePassword("testkey", "PA_p1", time.Time{})
	require.NoError(t, err)
	require.Equal(t, pw1, pw2)
}

func TestTURNAuthHandler_CreatePassword_DifferentParticipants(t *testing.T) {
	h := newTestAuthHandler()

	pw1, err := h.CreatePassword("testkey", "PA_p1", time.Time{})
	require.NoError(t, err)
	pw2, err := h.CreatePassword("testkey", "PA_p2", time.Time{})
	require.NoError(t, err)
	require.NotEqual(t, pw1, pw2)
}

func TestTURNAuthHandler_CreatePassword_InvalidKey(t *testing.T) {
	h := newTestAuthHandler()

	_, err := h.CreatePassword("unknownkey", "PA_p1", time.Time{})
	require.ErrorIs(t, err, ErrInvalidAPIKey)
}

// Verifies that the expiry is bound into the password hash. Two passwords for
// the same (apiKey, pID) pair but different expiries must differ, so that
// stripping the expiry from a leaked 3-part username cannot yield a captured
// password matching the server's legacy code path.
func TestTURNAuthHandler_CreatePassword_ExpiryBoundIntoHash(t *testing.T) {
	h := newTestAuthHandler()

	legacy, err := h.CreatePassword("testkey", "PA_p1", time.Time{})
	require.NoError(t, err)

	withExpiry, err := h.CreatePassword("testkey", "PA_p1", time.Unix(1_700_000_000, 0))
	require.NoError(t, err)
	require.NotEqual(t, legacy, withExpiry,
		"legacy-vs-expiry password must differ so stripping the expiry from a username "+
			"cannot yield a captured password")

	differentExpiry, err := h.CreatePassword("testkey", "PA_p1", time.Unix(1_700_003_600, 0))
	require.NoError(t, err)
	require.NotEqual(t, withExpiry, differentExpiry,
		"different expiries must produce different passwords; otherwise rotation by re-expiry would not change the credential")
}

// ---------------------------------------------------------------------------
// HandleAuth — basic credential acceptance / rejection
// ---------------------------------------------------------------------------

func TestTURNAuthHandler_HandleAuth_ValidCredentials(t *testing.T) {
	h := newTestAuthHandler()

	username := h.CreateUsername("testkey", "PA_p1", time.Time{})
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
	username := h.CreateUsername("unknownkey", "PA_p1", time.Time{})
	_, ok := h.HandleAuth(username, LivekitRealm, nil)
	require.False(t, ok)
}

func TestTURNAuthHandler_HandleAuth_DifferentParticipantsGetDifferentKeys(t *testing.T) {
	h := newTestAuthHandler()

	u1 := h.CreateUsername("testkey", "PA_p1", time.Time{})
	u2 := h.CreateUsername("testkey", "PA_p2", time.Time{})

	key1, ok1 := h.HandleAuth(u1, LivekitRealm, nil)
	key2, ok2 := h.HandleAuth(u2, LivekitRealm, nil)
	require.True(t, ok1)
	require.True(t, ok2)
	require.NotEqual(t, key1, key2)
}

// ---------------------------------------------------------------------------
// HandleAuth — TTL & expiry
// ---------------------------------------------------------------------------

func TestTURNAuthHandler_HandleAuth_ValidFresh(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)
	username, _, err := h.CreateCredentials("testkey", "PA_p1", time.Hour)
	require.NoError(t, err)

	// Fast-forward clock just past issuance: well before expiry.
	h.now = func() time.Time { return issuedAt.Add(5 * time.Minute) }

	_, ok := h.HandleAuth(username, LivekitRealm, nil)
	require.True(t, ok)
}

func TestTURNAuthHandler_HandleAuth_ValidAtBoundary(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)
	username, _, err := h.CreateCredentials("testkey", "PA_p1", time.Hour)
	require.NoError(t, err)

	// Exactly at expiry — strict After check means still valid.
	h.now = func() time.Time { return issuedAt.Add(time.Hour) }

	_, ok := h.HandleAuth(username, LivekitRealm, nil)
	require.True(t, ok)
}

func TestTURNAuthHandler_HandleAuth_ValidWithinSkew(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)
	username, _, err := h.CreateCredentials("testkey", "PA_p1", time.Hour)
	require.NoError(t, err)

	// 2m past expiry — inside the 5m skew tolerance.
	h.now = func() time.Time { return issuedAt.Add(time.Hour + 2*time.Minute) }

	_, ok := h.HandleAuth(username, LivekitRealm, nil)
	require.True(t, ok)
}

func TestTURNAuthHandler_HandleAuth_ExpiredBeyondSkew(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)
	username, _, err := h.CreateCredentials("testkey", "PA_p1", time.Hour)
	require.NoError(t, err)

	// 5m + 1s past expiry — beyond skew, must reject.
	h.now = func() time.Time { return issuedAt.Add(time.Hour + 5*time.Minute + time.Second) }

	_, ok := h.HandleAuth(username, LivekitRealm, nil)
	require.False(t, ok)
}

// When the operator opts out of TTL (CredentialTTL=0), both username and
// password use the legacy no-expiry form consistently, so authentication
// works indefinitely. This is the documented backward-compat behavior.
func TestTURNAuthHandler_HandleAuth_LegacyModeNeverExpires(t *testing.T) {
	h := newTestAuthHandler()
	username, _, err := h.CreateCredentials("testkey", "PA_p1", 0) // opt-out mode
	require.NoError(t, err)

	// Advance clock arbitrarily far; legacy-mode creds must still authenticate.
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
// CreateCredentials — atomic (username, password) pair
// ---------------------------------------------------------------------------

// CreateCredentials must emit a username+password that round-trip end-to-end:
// the key HandleAuth returns (derived from the parsed username) must equal the
// key a legitimate client computes with the returned password. If these drift
// the TURN server will reject every legitimate allocation.
func TestTURNAuthHandler_CreateCredentials_RoundTripsThroughHandleAuth(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	for _, ttl := range []time.Duration{0, time.Hour, 24 * time.Hour} {
		username, password, err := h.CreateCredentials("testkey", "PA_p1", ttl)
		require.NoError(t, err)
		require.NotEmpty(t, username)
		require.NotEmpty(t, password)

		serverKey, ok := h.HandleAuth(username, LivekitRealm, nil)
		require.True(t, ok, "ttl=%v", ttl)
		clientKey := turn.GenerateAuthKey(username, LivekitRealm, password)
		require.Equal(t, clientKey, serverKey, "ttl=%v: server/client keys must agree", ttl)
	}
}

func TestTURNAuthHandler_CreateCredentials_UnknownAPIKey(t *testing.T) {
	h := newTestAuthHandler()
	_, _, err := h.CreateCredentials("unknownkey", "PA_p1", time.Hour)
	require.ErrorIs(t, err, ErrInvalidAPIKey)
}

// ---------------------------------------------------------------------------
// Tampering — expiry stripping & extension
// ---------------------------------------------------------------------------

// Models a captured legitimate (username, password) pair issued with a TTL
// (3-part username `apiKey|pID|expiry`): the username is base62-decoded and
// re-encoded as the 2-part legacy form `apiKey|pID`, while the captured
// password is presented unchanged on the wire. pion/turn verifies
// MESSAGE-INTEGRITY as MD5(wire_username:realm:password), so if the server's
// recomputed password were unchanged by stripping the expiry the auth key
// would match and the leaked credential would authenticate indefinitely,
// defeating the TTL.
//
// CreatePassword binds the expiry into the hash. On the stripped-username
// path the server recomputes the LEGACY password (expiry zero) while the
// captured WITH-EXPIRY password no longer matches. The keys differ and
// MESSAGE-INTEGRITY fails.
func TestTURNAuthHandler_HandleAuth_ExpiryStrippingRejected(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	// Server issues a time-bound credential (3-part username + expiry-bound password).
	legitUsername, legitPassword, err := h.CreateCredentials("testkey", "PA_p1", time.Hour)
	require.NoError(t, err)

	// The legitimate cred authenticates and produces a matching key.
	serverKey, ok := h.HandleAuth(legitUsername, LivekitRealm, nil)
	require.True(t, ok)
	clientKey := turn.GenerateAuthKey(legitUsername, LivekitRealm, legitPassword)
	require.Equal(t, clientKey, serverKey, "legitimate creds must authenticate end-to-end")

	// Strip the expiry, re-encoding as the legacy 2-part form. The captured
	// password (bound to the original expiry) is presented unchanged.
	strippedUsername := h.CreateUsername("testkey", "PA_p1", time.Time{})

	// The server still accepts the 2-part format at the parse layer...
	serverKeyAfterStrip, parseOk := h.HandleAuth(strippedUsername, LivekitRealm, nil)
	require.True(t, parseOk, "parse layer accepts the 2-part form (this is by design; the real gate is the password hash)")

	// ...but the server's recomputed key uses the LEGACY password (no expiry
	// in hash), while the replayed key uses the WITH-EXPIRY password.
	// They must differ — otherwise the stripping bypass would succeed.
	replayedKey := turn.GenerateAuthKey(strippedUsername, LivekitRealm, legitPassword)
	require.NotEqual(t, replayedKey, serverKeyAfterStrip,
		"SECURITY: stripping the expiry from a leaked 3-part username must produce a password mismatch; "+
			"if these keys are equal, the TTL is bypassable and any leaked credential lasts forever")
}

// Variant of the expiry-stripping scenario: even when stripping happens
// pre-emptively while the original expiry is still valid (within skew),
// it must still produce a mismatched server-side password.
func TestTURNAuthHandler_StripExpiry_WithinSkew(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	_, legitPassword, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)

	stripped := encodeUsername("testkey", "PA_alice")

	// Move clock to just inside the original expiry — stripping isn't needed for validity,
	// but it might be attempted preemptively before the original expiry arrives.
	h.now = func() time.Time { return issuedAt.Add(30 * time.Minute) }
	require.True(t, tamperedAuthRejected(h, stripped, legitPassword),
		"SECURITY: stripped username + captured with-expiry password authenticated even pre-expiry (hash not expiry-bound?)")
}

// A captured 3-part credential cannot have its lifetime extended by rewriting
// the expiry in the username, because the expiry is bound into the password
// hash.
func TestTURNAuthHandler_ExpiryExtensionRejected(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	legitUsername, legitPassword, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)

	// Original expiry was issuedAt + 1h. Try rewriting it.
	for _, newExpiry := range []int64{
		issuedAt.Add(100 * time.Hour).Unix(),      // far future
		issuedAt.Add(365 * 24 * time.Hour).Unix(), // a year
		math.MaxInt64,                             // overflow candidate
	} {
		tampered := encodeUsername("testkey", "PA_alice", fmt.Sprintf("%d", newExpiry))
		// Move clock past the original expiry so only tampered expiry could save it.
		h.now = func() time.Time { return issuedAt.Add(2 * time.Hour) }
		require.True(t, tamperedAuthRejected(h, tampered, legitPassword),
			"SECURITY: rewritten expiry %d authenticated — TTL bypassed", newExpiry)
	}

	// sanity: the legitimate creds still authenticate within skew
	h.now = func() time.Time { return issuedAt.Add(30 * time.Minute) }
	_, ok := h.HandleAuth(legitUsername, LivekitRealm, nil)
	require.True(t, ok)
}

// Tests expiry values that make Go's `time.Time.IsZero()` return true
// (namely, Unix seconds = -62135596800, which corresponds to Go's zero time:
// Jan 1, year 1 UTC). If the server's parse+hash paths were inconsistent
// here, the expiry check would be bypassed AND the server would compute a
// legacy-shaped password — which would match a legacy password that may
// have separately leaked.
func TestTURNAuthHandler_ZeroTimeExpiry_NotBypassable(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	// Hold the WITH-EXPIRY password as if previously captured.
	_, legitPassword, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)

	// Jan 1, year 1 UTC — Go's IsZero() returns true for time.Unix(-62135596800, 0).
	tampered := encodeUsername("testkey", "PA_alice", "-62135596800")

	h.now = func() time.Time { return issuedAt.Add(100 * time.Hour) }
	require.True(t, tamperedAuthRejected(h, tampered, legitPassword),
		"SECURITY: zero-time expiry allowed a leaked 3-part password to authenticate indefinitely")
}

// In legacy mode (TTL=0) the operator opts out of expiry. A client encoding a
// 3-part username with the zero-time expiry is equivalent to the 2-part legacy
// form: both derive the same auth key from the legacy password. This test
// documents the invariant so future changes don't accidentally alter it —
// silent rotation breakage would be hard to diagnose.
func TestTURNAuthHandler_ZeroTimeExpiry_LegacyEquivalence(t *testing.T) {
	h := newTestAuthHandler()

	// Operator is running legacy mode (TTL=0). Legit 2-part credential.
	_, legacyPassword, err := h.CreateCredentials("testkey", "PA_alice", 0)
	require.NoError(t, err)

	// Client encodes a 3-part username with expiry = Go's zero time.
	disguised := encodeUsername("testkey", "PA_alice", "-62135596800")

	serverKey, ok := h.HandleAuth(disguised, LivekitRealm, nil)
	require.True(t, ok, "server accepts 3-part-zero form as equivalent to 2-part legacy")
	clientKey := turn.GenerateAuthKey(disguised, LivekitRealm, legacyPassword)
	require.Equal(t, string(clientKey), string(serverKey),
		"legacy password must derive the same auth key under 3-part-zero encoding (consistency invariant)")
}

// ---------------------------------------------------------------------------
// Tampering — credential reuse & replay
// ---------------------------------------------------------------------------

// Alice cannot use her credential to authenticate as Bob by rewriting pID.
// The pID is bound into the password hash.
func TestTURNAuthHandler_CrossParticipant_PIDForgeryRejected(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	_, alicePassword, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)

	// Anyone with Alice's captured creds tries to authenticate as someone else
	// by rewriting the pID in the username.
	for _, targetPID := range []string{
		"PA_bob",
		"PA_admin",
		"PA_", // empty suffix
		"",    // empty
		"*",   // wildcard-ish
	} {
		// Preserve the original expiry so the expiry check is not the reason for rejection.
		tampered := encodeUsername("testkey", targetPID, fmt.Sprintf("%d", issuedAt.Add(time.Hour).Unix()))
		require.True(t, tamperedAuthRejected(h, tampered, alicePassword),
			"SECURITY: Alice impersonated pID %q using her own captured password", targetPID)
	}
}

// A credential issued against apiKey=K1 must not authenticate against a server
// configured with a different (K1-unknown OR K2-with-different-secret) key.
func TestTURNAuthHandler_CrossAPIKey_Rejected(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)

	// Server 1: issues credential for Alice under apiKey "K1"/secret "S1".
	h1 := &TURNAuthHandler{
		keyProvider: auth.NewSimpleKeyProvider("K1", "S1"),
		now:         func() time.Time { return issuedAt },
	}
	aliceUsername, alicePassword, err := h1.CreateCredentials("K1", "PA_alice", time.Hour)
	require.NoError(t, err)

	// Server 2: same apiKey name, different secret.
	h2 := &TURNAuthHandler{
		keyProvider: auth.NewSimpleKeyProvider("K1", "DIFFERENT_SECRET"),
		now:         func() time.Time { return issuedAt.Add(5 * time.Minute) },
	}
	require.True(t, tamperedAuthRejected(h2, aliceUsername, alicePassword),
		"SECURITY: credential authenticated on a server with a different secret for the same apiKey")

	// Server 3: doesn't know this apiKey at all.
	h3 := &TURNAuthHandler{
		keyProvider: auth.NewSimpleKeyProvider("OTHER_KEY", "S1"),
		now:         func() time.Time { return issuedAt.Add(5 * time.Minute) },
	}
	require.True(t, tamperedAuthRejected(h3, aliceUsername, alicePassword),
		"SECURITY: credential authenticated on a server that doesn't know the apiKey")
}

// Pure replay of the captured credential after expiry+skew must fail.
func TestTURNAuthHandler_ReplayAfterExpiry_Rejected(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	legitUsername, legitPassword, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)

	// Advance beyond skew.
	h.now = func() time.Time { return issuedAt.Add(time.Hour + turnExpirySkew + time.Second) }
	require.True(t, tamperedAuthRejected(h, legitUsername, legitPassword),
		"SECURITY: pure replay past expiry+skew authenticated (TTL not enforced)")
}

// ---------------------------------------------------------------------------
// Input validation — numeric edges, byte-layer tampering, operator misconfig
// ---------------------------------------------------------------------------

// Tampered username carries max int64 expiry; time arithmetic inside HandleAuth
// must not panic and must not authenticate without the correct password.
func TestTURNAuthHandler_MaxInt64Expiry_NoPanicNoBypass(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	_, legitPassword, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)

	tampered := encodeUsername("testkey", "PA_alice", fmt.Sprintf("%d", int64(math.MaxInt64)))

	require.NotPanics(t, func() {
		require.True(t, tamperedAuthRejected(h, tampered, legitPassword),
			"SECURITY: MaxInt64 expiry allowed captured password to authenticate")
	}, "MaxInt64 expiry must not panic on clock arithmetic")
}

// Tampered username carries a negative expiry. Server must reject
// (now.After == true) AND captured password must not match.
func TestTURNAuthHandler_NegativeExpiry_Rejected(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	_, legitPassword, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)

	for _, negExpiry := range []int64{-1, -1000, math.MinInt64} {
		tampered := encodeUsername("testkey", "PA_alice", fmt.Sprintf("%d", negExpiry))
		require.NotPanics(t, func() {
			require.True(t, tamperedAuthRejected(h, tampered, legitPassword),
				"SECURITY: negative expiry %d allowed captured password to authenticate", negExpiry)
		})
	}
}

// A null byte injected into the pID must not match any legitimate pID.
func TestTURNAuthHandler_NullByteInPID_Rejected(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	_, legitPassword, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)

	tampered := encodeUsername("testkey", "PA_alice\x00", fmt.Sprintf("%d", issuedAt.Add(time.Hour).Unix()))
	require.True(t, tamperedAuthRejected(h, tampered, legitPassword),
		"SECURITY: null-byte injection in pID allowed captured password to authenticate")
}

// A username whose decoded plaintext contains a `|` within the pID produces
// an ambiguous 4-part split. Server must reject.
func TestTURNAuthHandler_PipeInjectionInPID_Rejected(t *testing.T) {
	h := newTestAuthHandler()

	// 4-part plaintext: "apiKey|PA_foo|PA_bar|1700000000". ParseUsername must
	// treat this as invalid.
	encoded := encodeUsername("testkey", "PA_foo", "PA_bar", "1700000000")
	_, ok := h.HandleAuth(encoded, LivekitRealm, nil)
	require.False(t, ok, "SECURITY: ambiguous 4-part username was accepted")
}

// 1-part and 5+ part username forms must be rejected.
func TestTURNAuthHandler_WrongNumberOfParts_Rejected(t *testing.T) {
	h := newTestAuthHandler()

	cases := []struct {
		name  string
		parts []string
	}{
		{"1-part", []string{"loneword"}},
		{"5-part", []string{"a", "b", "c", "d", "e"}},
		{"6-part", []string{"a", "b", "c", "d", "e", "f"}},
		{"empty-single", []string{""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			encoded := encodeUsername(c.parts...)
			_, ok := h.HandleAuth(encoded, LivekitRealm, nil)
			require.False(t, ok, "SECURITY: %s username was accepted", c.name)
		})
	}
}

// If the server treated empty pID as a wildcard or looked up a default secret
// under the empty apiKey, this would be catastrophic. Verify neither happens.
func TestTURNAuthHandler_EmptyFields_Rejected(t *testing.T) {
	h := newTestAuthHandler()

	// Capture a legit credential for a real participant.
	_, realPassword, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)

	// Try various empty-field combinations.
	expiryStr := fmt.Sprintf("%d", time.Now().Add(time.Hour).Unix())
	cases := [][]string{
		{"", "PA_alice", expiryStr}, // empty apiKey
		{"testkey", "", expiryStr},  // empty pID
		{"", "", expiryStr},         // both empty
		{"", "PA_alice"},            // 2-part empty apiKey
		{"testkey", ""},             // 2-part empty pID
		{"", ""},                    // 2-part both empty
	}
	for i, parts := range cases {
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			tampered := encodeUsername(parts...)
			require.True(t, tamperedAuthRejected(h, tampered, realPassword),
				"SECURITY: empty-field form %v authenticated with real password", parts)
		})
	}
}

// If the operator configures an apiKey or pID containing the '|' separator or
// a NUL byte, CreateCredentials must refuse rather than mint a credential
// that won't round-trip. Without this validation the misconfiguration would
// only surface at TURN connect time as opaque auth failures.

func TestTURNAuthHandler_RejectsSeparatorInAPIKey(t *testing.T) {
	h := &TURNAuthHandler{
		keyProvider: auth.NewSimpleKeyProvider("key|with|pipe", "secret"),
		now:         time.Now,
	}
	_, _, err := h.CreateCredentials("key|with|pipe", "PA_alice", time.Hour)
	require.ErrorIs(t, err, ErrInvalidCredentialInput)
}

func TestTURNAuthHandler_RejectsSeparatorInPID(t *testing.T) {
	h := newTestAuthHandler()
	_, _, err := h.CreateCredentials("testkey", livekit.ParticipantID("PA_alice|bob"), time.Hour)
	require.ErrorIs(t, err, ErrInvalidCredentialInput)
}

func TestTURNAuthHandler_RejectsNullByteInAPIKey(t *testing.T) {
	h := &TURNAuthHandler{
		keyProvider: auth.NewSimpleKeyProvider("key\x00nul", "secret"),
		now:         time.Now,
	}
	_, _, err := h.CreateCredentials("key\x00nul", "PA_alice", time.Hour)
	require.ErrorIs(t, err, ErrInvalidCredentialInput)
}

func TestTURNAuthHandler_RejectsNullByteInPID(t *testing.T) {
	h := newTestAuthHandler()
	_, _, err := h.CreateCredentials("testkey", livekit.ParticipantID("PA_alice\x00"), time.Hour)
	require.ErrorIs(t, err, ErrInvalidCredentialInput)
}

// Normal inputs continue to work after the validation.
func TestTURNAuthHandler_AcceptsNormalInputs(t *testing.T) {
	h := newTestAuthHandler()
	_, _, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)
	_, _, err = h.CreateCredentials("testkey", "PA_alice", 0) // legacy
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// resolveTURNRelayAddress
// ---------------------------------------------------------------------------

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

// END OPENVIDU BLOCK
