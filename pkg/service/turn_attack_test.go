// BEGIN OPENVIDU BLOCK
// Copyright 2026 OpenVidu
//
// Licensed under the Apache License, Version 2.0 (the "License").
//
// Offensive security regression tests for the TURN long-term credential
// scheme. Each test models a concrete attacker capability (captured
// credential, wire-layer tampering, parser edge cases) and asserts that the
// authentication fails end-to-end. Failures in this file indicate a real
// exploitable bypass — treat them as P0.

package service

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/jxskiss/base62"
	"github.com/pion/turn/v4"
	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
)

// attackDefeated encapsulates the end-to-end check that an attacker holding
// a captured (username, password) pair cannot authenticate by replaying the
// password with a tampered username. Either:
//   - HandleAuth rejects the tampered username outright, OR
//   - HandleAuth accepts it at the parse layer but the server-derived key
//     differs from the attacker's replayed key (MESSAGE-INTEGRITY fails in
//     pion/turn).
//
// Returns true iff the attack is defeated.
func attackDefeated(h *TURNAuthHandler, tamperedUsername string, capturedPassword string) bool {
	serverKey, ok := h.HandleAuth(tamperedUsername, LivekitRealm, nil)
	if !ok {
		return true // rejected at parse / expiry / key-lookup layer
	}
	attackerKey := turn.GenerateAuthKey(tamperedUsername, LivekitRealm, capturedPassword)
	return string(attackerKey) != string(serverKey)
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
// ATTACK: expiry tampering — captured cred, attacker mutates expiry value.
// ---------------------------------------------------------------------------

// An attacker who captures a 3-part credential cannot extend its lifetime by
// rewriting the expiry in the username, because the expiry is bound into the
// password hash.
func TestAttack_ExpiryExtension(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	legitUsername, legitPassword, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)

	// Original expiry was issuedAt + 1h. Attacker tries to rewrite it.
	for _, newExpiry := range []int64{
		issuedAt.Add(100 * time.Hour).Unix(),   // far future
		issuedAt.Add(365 * 24 * time.Hour).Unix(), // a year
		math.MaxInt64,                            // overflow candidate
	} {
		tampered := encodeUsername("testkey", "PA_alice", fmt.Sprintf("%d", newExpiry))
		// Move clock past the original expiry so only tampered expiry could save it.
		h.now = func() time.Time { return issuedAt.Add(2 * time.Hour) }
		require.True(t, attackDefeated(h, tampered, legitPassword),
			"SECURITY: attacker extended expiry to %d and authenticated — TTL bypassed", newExpiry)
	}

	// sanity: the legitimate creds still authenticate within skew
	h.now = func() time.Time { return issuedAt.Add(30 * time.Minute) }
	_, ok := h.HandleAuth(legitUsername, LivekitRealm, nil)
	require.True(t, ok)
}

// Attacker rewrites the expiry to values that make Go's `time.Time.IsZero()`
// return true (namely, Unix seconds = -62135596800, which corresponds to
// Go's zero time: Jan 1, year 1 UTC). If the server's parse+hash paths were
// inconsistent here, the expiry check would be bypassed AND the server
// would compute a legacy-shaped password — which matches the legacy
// password an attacker might separately hold.
func TestAttack_ZeroTimeExpiry_Bypass(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	// Attacker holds the WITH-EXPIRY password.
	_, legitPassword, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)

	// Jan 1, year 1 UTC — Go's IsZero() returns true for time.Unix(-62135596800, 0).
	tampered := encodeUsername("testkey", "PA_alice", "-62135596800")

	h.now = func() time.Time { return issuedAt.Add(100 * time.Hour) }
	require.True(t, attackDefeated(h, tampered, legitPassword),
		"SECURITY: zero-time expiry allowed a leaked 3-part password to authenticate indefinitely")
}

// Attacker with a LEGACY (2-part) password tries to disguise it as a 3-part
// cred with a zero-time expiry. The server's expiry check is bypassed (IsZero)
// but this attack "succeeds" only in the harmless sense that the legacy-form
// password is still valid — which it is by design (opt-out mode). This test
// documents the invariant so future changes don't accidentally alter it.
func TestAttack_ZeroTimeExpiry_LegacyPasswordEquivalence(t *testing.T) {
	h := newTestAuthHandler()

	// Operator is running legacy mode (TTL=0). Legit 2-part credential.
	_, legacyPassword, err := h.CreateCredentials("testkey", "PA_alice", 0)
	require.NoError(t, err)

	// Client encodes a 3-part username with expiry = Go's zero time.
	disguised := encodeUsername("testkey", "PA_alice", "-62135596800")

	// Legacy credential is equivalent whether presented as 2-part or 3-part-zero.
	// This is EXPECTED: the invariant is "zero-expiry password is the legacy password,
	// regardless of on-wire encoding". If this ever changes, rotations break silently.
	serverKey, ok := h.HandleAuth(disguised, LivekitRealm, nil)
	require.True(t, ok, "server accepts 3-part-zero disguise as equivalent to 2-part legacy")
	attackerKey := turn.GenerateAuthKey(disguised, LivekitRealm, legacyPassword)
	require.Equal(t, string(attackerKey), string(serverKey),
		"legacy password must derive the same auth key under 3-part-zero encoding (consistency invariant)")
}

// ---------------------------------------------------------------------------
// ATTACK: cross-participant credential reuse
// ---------------------------------------------------------------------------

// Alice cannot use her credential to authenticate as Bob by rewriting pID.
// The pID is bound into the password hash.
func TestAttack_CrossParticipant_ForgePID(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	aliceUsername, alicePassword, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)
	_ = aliceUsername

	// Attacker (Alice herself, or anyone with her captured creds) tries to
	// authenticate as Bob by rewriting the pID in the username.
	for _, targetPID := range []string{
		"PA_bob",
		"PA_admin",
		"PA_",     // empty suffix
		"",        // empty
		"*",       // wildcard-ish
	} {
		// Preserve the original expiry so the expiry check is not the reason for rejection.
		tampered := encodeUsername("testkey", targetPID, fmt.Sprintf("%d", issuedAt.Add(time.Hour).Unix()))
		require.True(t, attackDefeated(h, tampered, alicePassword),
			"SECURITY: Alice impersonated pID %q using her own captured password", targetPID)
	}
}

// ---------------------------------------------------------------------------
// ATTACK: cross-apiKey credential reuse
// ---------------------------------------------------------------------------

// A credential issued against apiKey=K1 must not authenticate against a server
// configured with a different (K1-unknown OR K2-with-different-secret) key.
func TestAttack_CrossAPIKey_DifferentSecret(t *testing.T) {
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
	require.True(t, attackDefeated(h2, aliceUsername, alicePassword),
		"SECURITY: credential authenticated on a server with a different secret for the same apiKey")

	// Server 3: doesn't know this apiKey at all.
	h3 := &TURNAuthHandler{
		keyProvider: auth.NewSimpleKeyProvider("OTHER_KEY", "S1"),
		now:         func() time.Time { return issuedAt.Add(5 * time.Minute) },
	}
	require.True(t, attackDefeated(h3, aliceUsername, alicePassword),
		"SECURITY: credential authenticated on a server that doesn't know the apiKey")
}

// ---------------------------------------------------------------------------
// ATTACK: expiry overflow / negative values
// ---------------------------------------------------------------------------

// Attacker passes max int64 expiry; time arithmetic inside HandleAuth must not
// panic and must not authenticate without the correct password.
func TestAttack_MaxInt64Expiry_NoPanic_NoBypass(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	_, legitPassword, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)

	tampered := encodeUsername("testkey", "PA_alice", fmt.Sprintf("%d", int64(math.MaxInt64)))

	require.NotPanics(t, func() {
		require.True(t, attackDefeated(h, tampered, legitPassword),
			"SECURITY: MaxInt64 expiry allowed captured password to authenticate")
	}, "MaxInt64 expiry must not panic on clock arithmetic")
}

// Attacker passes a negative expiry. Server must reject (now.After == true)
// AND captured password must not match.
func TestAttack_NegativeExpiry(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	_, legitPassword, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)

	for _, negExpiry := range []int64{-1, -1000, math.MinInt64} {
		tampered := encodeUsername("testkey", "PA_alice", fmt.Sprintf("%d", negExpiry))
		require.NotPanics(t, func() {
			require.True(t, attackDefeated(h, tampered, legitPassword),
				"SECURITY: negative expiry %d allowed captured password to authenticate", negExpiry)
		})
	}
}

// ---------------------------------------------------------------------------
// ATTACK: username tampering at the bytes layer
// ---------------------------------------------------------------------------

// Attacker injects a null byte into the pID. Must not match any legitimate pID.
func TestAttack_NullByteInPID(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	_, legitPassword, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)

	tampered := encodeUsername("testkey", "PA_alice\x00", fmt.Sprintf("%d", issuedAt.Add(time.Hour).Unix()))
	require.True(t, attackDefeated(h, tampered, legitPassword),
		"SECURITY: null-byte injection in pID allowed captured password to authenticate")
}

// Attacker attempts to craft a username whose decoded plaintext contains a
// `|` within the pID, producing an ambiguous 4-part split. Server must reject.
func TestAttack_PipeInjectionInPID(t *testing.T) {
	h := newTestAuthHandler()

	// 4-part plaintext: "apiKey|PA_foo|PA_bar|1700000000". ParseUsername must
	// treat this as invalid.
	encoded := encodeUsername("testkey", "PA_foo", "PA_bar", "1700000000")
	_, ok := h.HandleAuth(encoded, LivekitRealm, nil)
	require.False(t, ok, "SECURITY: ambiguous 4-part username was accepted")
}

// Attacker attempts 1-part and 5+ part forms.
func TestAttack_WrongNumberOfParts(t *testing.T) {
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

// ---------------------------------------------------------------------------
// ATTACK: stripped-username regression (companion to existing test)
// ---------------------------------------------------------------------------

// Variant of the expiry-stripping attack — tests that even after clock drift
// within skew, stripping still fails.
func TestAttack_StripExpiry_WithinSkew(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	_, legitPassword, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)

	stripped := encodeUsername("testkey", "PA_alice")

	// Move clock to just inside the original expiry — stripping isn't needed for validity,
	// but attacker might try it preemptively before expiry arrives.
	h.now = func() time.Time { return issuedAt.Add(30 * time.Minute) }
	require.True(t, attackDefeated(h, stripped, legitPassword),
		"SECURITY: stripped username + captured with-expiry password authenticated even pre-expiry (hash not expiry-bound?)")
}

// ---------------------------------------------------------------------------
// ATTACK: replay after expiry (bearer token TTL enforcement)
// ---------------------------------------------------------------------------

// Pure replay of the captured credential after expiry+skew must fail.
func TestAttack_ReplayAfterExpiry(t *testing.T) {
	issuedAt := time.Unix(1_700_000_000, 0)
	h := newTestAuthHandlerAt(issuedAt)

	legitUsername, legitPassword, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)

	// Advance beyond skew.
	h.now = func() time.Time { return issuedAt.Add(time.Hour + turnExpirySkew + time.Second) }
	require.True(t, attackDefeated(h, legitUsername, legitPassword),
		"SECURITY: pure replay past expiry+skew authenticated (TTL not enforced)")
}

// ---------------------------------------------------------------------------
// ATTACK: empty-string pID smuggling via 3-part form
// ---------------------------------------------------------------------------

// If the server treated empty pID as a wildcard or looked up a default secret
// under the empty apiKey, this would be catastrophic. Verify neither happens.
func TestAttack_EmptyFields(t *testing.T) {
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
			require.True(t, attackDefeated(h, tampered, realPassword),
				"SECURITY: empty-field form %v authenticated with real password", parts)
		})
	}
}

// ---------------------------------------------------------------------------
// HARDENING: operator misconfiguration must fail loudly, not silently
// ---------------------------------------------------------------------------

// If the operator configures an apiKey or pID containing the '|' separator or
// a NUL byte, CreateCredentials must refuse rather than mint a credential that
// won't round-trip. Pre-fix behavior was silent success + auth failures at
// connect time, which is hard to diagnose in production.

func TestHardening_RejectsSeparatorInAPIKey(t *testing.T) {
	h := &TURNAuthHandler{
		keyProvider: auth.NewSimpleKeyProvider("key|with|pipe", "secret"),
		now:         time.Now,
	}
	_, _, err := h.CreateCredentials("key|with|pipe", "PA_alice", time.Hour)
	require.ErrorIs(t, err, ErrInvalidCredentialInput)
}

func TestHardening_RejectsSeparatorInPID(t *testing.T) {
	h := newTestAuthHandler()
	_, _, err := h.CreateCredentials("testkey", livekit.ParticipantID("PA_alice|bob"), time.Hour)
	require.ErrorIs(t, err, ErrInvalidCredentialInput)
}

func TestHardening_RejectsNullByteInAPIKey(t *testing.T) {
	h := &TURNAuthHandler{
		keyProvider: auth.NewSimpleKeyProvider("key\x00nul", "secret"),
		now:         time.Now,
	}
	_, _, err := h.CreateCredentials("key\x00nul", "PA_alice", time.Hour)
	require.ErrorIs(t, err, ErrInvalidCredentialInput)
}

func TestHardening_RejectsNullByteInPID(t *testing.T) {
	h := newTestAuthHandler()
	_, _, err := h.CreateCredentials("testkey", livekit.ParticipantID("PA_alice\x00"), time.Hour)
	require.ErrorIs(t, err, ErrInvalidCredentialInput)
}

// Normal inputs continue to work after the new validation.
func TestHardening_AcceptsNormalInputs(t *testing.T) {
	h := newTestAuthHandler()
	_, _, err := h.CreateCredentials("testkey", "PA_alice", time.Hour)
	require.NoError(t, err)
	_, _, err = h.CreateCredentials("testkey", "PA_alice", 0) // legacy
	require.NoError(t, err)
}

// END OPENVIDU BLOCK
