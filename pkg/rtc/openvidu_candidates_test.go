// BEGIN OPENVIDU BLOCK
package rtc

import (
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/transport/v4/vnet"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/livekit/mediatransportutil/pkg/rtcconfig"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/logger/pionlogger"
)

// newTestVNet builds a standalone virtual network with a single static local IP, so ICE gathering
// is deterministic and independent of the host's real interfaces.
func newTestVNet(t *testing.T, localIP string) *vnet.Net {
	t.Helper()

	router, err := vnet.NewRouter(&vnet.RouterConfig{
		CIDR:          "10.0.0.0/24",
		LoggerFactory: pionlogger.NewLoggerFactory(logger.GetLogger()),
	})
	require.NoError(t, err)

	nw, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{localIP}})
	require.NoError(t, err)
	require.NoError(t, router.AddNet(nw))
	require.NoError(t, router.Start())
	t.Cleanup(func() { assert.NoError(t, router.Stop()) })

	return nw
}

// gatherHostAddresses gathers ICE candidates using the given setting engine and returns the
// addresses of the host candidates. Mirrors pion's internal gatherCandidatesWithSettingEngine,
// which is not exported.
func gatherHostAddresses(t *testing.T, se webrtc.SettingEngine) []string {
	t.Helper()

	gatherer, err := webrtc.NewAPI(webrtc.WithSettingEngine(se)).NewICEGatherer(webrtc.ICEGatherOptions{})
	require.NoError(t, err)

	done := make(chan struct{})
	var candidates []webrtc.ICECandidate
	gatherer.OnLocalCandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			close(done)
			return
		}
		candidates = append(candidates, *c)
	})

	require.NoError(t, gatherer.Gather())
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "gather did not complete")
	}
	require.NoError(t, gatherer.Close())

	var hostAddrs []string
	for _, c := range candidates {
		if c.Typ == webrtc.ICECandidateTypeHost {
			hostAddrs = append(hostAddrs, c.Address)
		}
	}
	return hostAddrs
}

// TestSetNAT1To1AddressRewriteRulesIncludeInternal_AdvertisesBothIPs verifies that
// rtcconfig.SetNAT1To1AddressRewriteRules with includeInternal=true advertises BOTH the mapped
// external IP and the internal IP as host candidates (the advertise_internal_ip behavior). The
// fork relies on this append semantic, so this guards it against future module bumps.
func TestSetNAT1To1AddressRewriteRulesIncludeInternal_AdvertisesBothIPs(t *testing.T) {
	const (
		localIP    = "10.0.0.2"
		externalIP = "203.0.113.30"
	)
	nw := newTestVNet(t, localIP)

	se := webrtc.SettingEngine{}
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	se.SetNet(nw)

	require.NoError(t, rtcconfig.SetNAT1To1AddressRewriteRules(&se, []string{externalIP + "/" + localIP}, true))

	hostAddrs := gatherHostAddresses(t, se)
	require.NotEmpty(t, hostAddrs, "expected host candidates")
	assert.Contains(t, hostAddrs, externalIP, "external IP must be advertised as host")
	assert.Contains(t, hostAddrs, localIP, "internal IP must also be advertised as host (append)")
}

// TestSetHostRewriteRulesReplace_HidesInternal documents the contrast: the default replace mode
// (what NewWebRTCConfig applies today when advertise_internal_ip is off) advertises the external IP
// only and hides the internal one.
func TestSetHostRewriteRulesReplace_HidesInternal(t *testing.T) {
	const (
		localIP    = "10.0.0.3"
		externalIP = "203.0.113.31"
	)
	nw := newTestVNet(t, localIP)

	se := webrtc.SettingEngine{}
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	se.SetNet(nw)

	require.NoError(t, se.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{
		External:        []string{externalIP},
		Local:           localIP,
		AsCandidateType: webrtc.ICECandidateTypeHost,
		Mode:            webrtc.ICEAddressRewriteReplace,
	}))

	hostAddrs := gatherHostAddresses(t, se)
	require.NotEmpty(t, hostAddrs, "expected host candidates")
	assert.Contains(t, hostAddrs, externalIP)
	assert.NotContains(t, hostAddrs, localIP)
}

// END OPENVIDU BLOCK
