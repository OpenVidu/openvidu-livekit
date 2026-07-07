// BEGIN OPENVIDU BLOCK
package rtc

import (
	"strings"

	"github.com/pion/webrtc/v4"
)

// SetHostRewriteRulesAppendingInternal implements the advertise_internal_ip behavior on top of the
// pinned pion/mediatransportutil versions. For each "external/local" NAT1To1 mapping it emits a host
// address rewrite rule in APPEND mode, so that BOTH the mapped external IP and the internal (local)
// IP are advertised as host candidates. This is what upstream LiveKit does via
// rtcconfig.SetNAT1To1AddressRewriteRules(..., includeInternal=true), which is only available in
// newer module versions. On a future rebase this helper can be dropped in favor of that call.
//
// It mirrors the pinned rtcconfig.SetNAT1To1AddressRewriteRules rule construction, only differing in
// that the per-mapping host rules use webrtc.ICEAddressRewriteAppend instead of the default
// (replace) mode. SetICEAddressRewriteRules replaces the full rule set on the setting engine, so this
// cleanly overrides the external-only rules set earlier by NewWebRTCConfig.
func SetHostRewriteRulesAppendingInternal(s *webrtc.SettingEngine, ips []string) error {
	rules := make([]webrtc.ICEAddressRewriteRule, 0, len(ips)+1)
	catchAll := make([]string, 0, len(ips))

	for _, ip := range ips {
		if parts := strings.Split(ip, "/"); len(parts) == 2 {
			rules = append(rules, webrtc.ICEAddressRewriteRule{
				External:        []string{parts[0]},
				Local:           parts[1],
				AsCandidateType: webrtc.ICECandidateTypeHost,
				Mode:            webrtc.ICEAddressRewriteAppend,
			})
		} else {
			catchAll = append(catchAll, ip)
		}
	}
	if len(catchAll) > 0 {
		rules = append(rules, webrtc.ICEAddressRewriteRule{
			External:        catchAll,
			AsCandidateType: webrtc.ICECandidateTypeHost,
		})
	}

	return s.SetICEAddressRewriteRules(rules...)
}

// END OPENVIDU BLOCK
