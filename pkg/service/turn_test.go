// Copyright 2026 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"net"
	"testing"

	"github.com/pion/stun/v3"
	"github.com/pion/turn/v5"
	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"

	// BEGIN OPENVIDU BLOCK
	"github.com/livekit/livekit-server/pkg/config"
	"github.com/livekit/mediatransportutil/pkg/rtcconfig"
	// END OPENVIDU BLOCK
)

const (
	turnTestAPIKey    = "APITestKey"
	turnTestAPISecret = "TestSecret"
)

func newTestTurnAuthHandler() *TURNAuthHandler {
	return NewTURNAuthHandler(auth.NewSimpleKeyProvider(turnTestAPIKey, turnTestAPISecret))
}

func mustAuthCreds(t *testing.T, h *TURNAuthHandler, pID livekit.ParticipantID, ttlSeconds int) (username string, key []byte) {
	t.Helper()
	username, expiry := h.CreateUsername(turnTestAPIKey, pID, ttlSeconds)
	password, err := h.CreatePassword(turnTestAPIKey, pID, expiry)
	require.NoError(t, err)
	return username, turn.GenerateAuthKey(username, LivekitRealm, password)
}

func TestTURNAuthHandler_HandleAuth_ValidCredentials(t *testing.T) {
	h := newTestTurnAuthHandler()
	pID := livekit.ParticipantID("PA_valid")
	username, expectedKey := mustAuthCreds(t, h, pID, 300)

	for _, method := range []stun.Method{
		stun.MethodAllocate,
		stun.MethodRefresh,
		stun.MethodCreatePermission,
		stun.MethodChannelBind,
		stun.MethodSend,
	} {
		t.Run(method.String(), func(t *testing.T) {
			userID, key, ok := h.HandleAuth(&turn.RequestAttributes{
				Username: username,
				Realm:    LivekitRealm,
				SrcAddr:  &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234},
				Method:   method,
			})
			require.True(t, ok)
			require.Equal(t, string(pID), userID)
			require.Equal(t, expectedKey, key)
		})
	}
}

func TestTURNAuthHandler_HandleAuth_ExpiredAllocateRejected(t *testing.T) {
	h := newTestTurnAuthHandler()
	pID := livekit.ParticipantID("PA_expired_alloc")

	username, _ := h.CreateUsername(turnTestAPIKey, pID, -60)
	_, _, ok := h.HandleAuth(&turn.RequestAttributes{
		Username: username,
		Realm:    LivekitRealm,
		SrcAddr:  &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234},
		Method:   stun.MethodAllocate,
	})
	require.False(t, ok, "Allocate request with expired credentials must be rejected")
}

func TestTURNAuthHandler_HandleAuth_ExpiredNonAllocateAllowed(t *testing.T) {
	h := newTestTurnAuthHandler()
	pID := livekit.ParticipantID("PA_expired_refresh")

	username, expiry := h.CreateUsername(turnTestAPIKey, pID, -60)

	// CreatePassword still enforces ErrExpired on its own, but the server hands
	// the same key it generated at allocation time — reproduce that by directly
	// hashing without going through CreatePassword's expiry guard.
	password, err := h.computePassword(turnTestAPIKey, pID, expiry)
	require.NoError(t, err)
	expectedKey := turn.GenerateAuthKey(username, LivekitRealm, password)

	for _, method := range []stun.Method{
		stun.MethodRefresh,
		stun.MethodCreatePermission,
		stun.MethodChannelBind,
		stun.MethodSend,
	} {
		t.Run(method.String(), func(t *testing.T) {
			userID, key, ok := h.HandleAuth(&turn.RequestAttributes{
				Username: username,
				Realm:    LivekitRealm,
				SrcAddr:  &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234},
				Method:   method,
			})
			require.True(t, ok, "Non-allocate request with expired credentials must succeed")
			require.Equal(t, string(pID), userID)
			require.Equal(t, expectedKey, key)
		})
	}
}

func TestTURNAuthHandler_HandleAuth_WrongUsernameRejected(t *testing.T) {
	h := newTestTurnAuthHandler()
	_, _, ok := h.HandleAuth(&turn.RequestAttributes{
		Username: "not-base62!!!",
		Realm:    LivekitRealm,
		SrcAddr:  &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234},
		Method:   stun.MethodRefresh,
	})
	require.False(t, ok)
}

// BEGIN OPENVIDU BLOCK

// ---------------------------------------------------------------------------
// resolveTURNRelayAddress
// ---------------------------------------------------------------------------

func TestResolveTURNRelayAddress(t *testing.T) {
	t.Run("explicit RelayAddress is always used", func(t *testing.T) {
		conf := &config.Config{}
		conf.TURN.RelayAddress = "10.0.0.99"
		conf.RTC.NodeIP = rtcconfig.NodeIP{V4: "203.0.113.1"}
		conf.PubliclyReachable = true

		addr, err := resolveTURNRelayAddress(conf)
		require.NoError(t, err)
		require.Equal(t, "10.0.0.99", addr)
	})

	t.Run("explicit RelayAddress used even when not publicly reachable", func(t *testing.T) {
		conf := &config.Config{}
		conf.TURN.RelayAddress = "10.0.0.99"
		conf.RTC.NodeIP = rtcconfig.NodeIP{V4: "192.168.1.1"}
		conf.PubliclyReachable = false

		addr, err := resolveTURNRelayAddress(conf)
		require.NoError(t, err)
		require.Equal(t, "10.0.0.99", addr)
	})

	t.Run("publicly reachable with no RelayAddress falls back to NodeIP", func(t *testing.T) {
		conf := &config.Config{}
		conf.RTC.NodeIP = rtcconfig.NodeIP{V4: "203.0.113.1"}
		conf.PubliclyReachable = true

		addr, err := resolveTURNRelayAddress(conf)
		require.NoError(t, err)
		require.Equal(t, "203.0.113.1", addr)
	})

	t.Run("not publicly reachable with no RelayAddress uses first local IP", func(t *testing.T) {
		localIPs, err := rtcconfig.GetLocalIPAddresses(false, false, nil, nil)
		if err != nil {
			t.Skipf("could not get local IP addresses: %v", err)
		}
		if len(localIPs) == 0 {
			t.Skip("no local IP addresses found")
		}

		conf := &config.Config{}
		conf.RTC.NodeIP = rtcconfig.NodeIP{V4: "203.0.113.1"}
		conf.PubliclyReachable = false

		addr, err := resolveTURNRelayAddress(conf)
		require.NoError(t, err)
		require.Equal(t, localIPs[0], addr)
	})
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
