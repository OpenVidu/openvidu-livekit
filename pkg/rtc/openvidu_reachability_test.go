// BEGIN OPENVIDU BLOCK
package rtc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/mediatransportutil/pkg/rtcconfig"
)

func TestIsPubliclyReachable(t *testing.T) {
	t.Run("returns true when NAT1To1IPs is configured", func(t *testing.T) {
		rtcConf := &rtcconfig.RTCConfig{NodeIP: rtcconfig.NodeIP{V4: "10.0.0.1"}}
		webrtcConf := &rtcconfig.WebRTCConfig{NAT1To1IPs: []string{"203.0.113.10"}}

		reachable, err := IsPubliclyReachable(rtcConf, webrtcConf)
		require.NoError(t, err)
		require.True(t, reachable)
	})

	t.Run("returns false when NodeIP matches a local IP and NAT1To1IPs is empty", func(t *testing.T) {
		localIPs, err := rtcconfig.GetLocalIPAddresses(false, false, nil, nil)
		if err != nil {
			t.Skipf("could not get local IP addresses: %v", err)
		}
		if len(localIPs) == 0 {
			t.Skip("no local IP addresses found")
		}

		rtcConf := &rtcconfig.RTCConfig{NodeIP: rtcconfig.NodeIP{V4: localIPs[0]}}
		webrtcConf := &rtcconfig.WebRTCConfig{}

		reachable, err := IsPubliclyReachable(rtcConf, webrtcConf)
		require.NoError(t, err)
		require.False(t, reachable)
	})

	t.Run("returns false when NodeIP is external but fails UDP hairpin validation", func(t *testing.T) {
		rtcConf := &rtcconfig.RTCConfig{NodeIP: rtcconfig.NodeIP{V4: "203.0.113.250"}}
		webrtcConf := &rtcconfig.WebRTCConfig{}

		reachable, err := IsPubliclyReachable(rtcConf, webrtcConf)
		require.NoError(t, err)
		require.False(t, reachable)
	})
}

func TestValidateExternalIP(t *testing.T) {
	t.Run("succeeds for loopback IP", func(t *testing.T) {
		err := ValidateExternalIP(context.Background(), "127.0.0.1", 0)
		require.NoError(t, err)
	})

	t.Run("fails for unreachable IP", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		err := ValidateExternalIP(ctx, "203.0.113.250", 0)
		require.Error(t, err)
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})
}

// END OPENVIDU BLOCK
