// BEGIN OPENVIDU BLOCK
package rtc

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/auth/authfakes"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/webhook"

	"github.com/livekit/livekit-server/pkg/config"
	"github.com/livekit/livekit-server/pkg/rtc/types"
	"github.com/livekit/livekit-server/pkg/rtc/types/typesfakes"
	"github.com/livekit/livekit-server/pkg/sfu"
	"github.com/livekit/livekit-server/pkg/telemetry"
	"github.com/livekit/livekit-server/pkg/telemetry/telemetryfakes"
	"github.com/livekit/livekit-server/version"
)

// openviduEgressLauncher records the identity of every participant egress started.
type openviduEgressLauncher struct {
	mu      sync.Mutex
	started []string
}

func (l *openviduEgressLauncher) StartEgress(_ context.Context, req *rpc.StartEgressRequest) (*livekit.EgressInfo, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.started = append(l.started, req.GetParticipant().GetIdentity())
	return &livekit.EgressInfo{}, nil
}

func (l *openviduEgressLauncher) StopEgress(context.Context, *livekit.StopEgressRequest) (*livekit.EgressInfo, error) {
	return &livekit.EgressInfo{}, nil
}

func (l *openviduEgressLauncher) count(identity string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, started := range l.started {
		if started == identity {
			n++
		}
	}
	return n
}

// One Auto Participant Egress per participant: a full reconnection keeps it,
// leaving and joining again starts a new one. With and without transcoding.
func TestAutoParticipantEgressAcrossSessions(t *testing.T) {
	for _, preset := range []livekit.EncodingOptionsPreset{
		livekit.EncodingOptionsPreset_PASSTHROUGH,
		livekit.EncodingOptionsPreset_H264_720P_30,
	} {
		t.Run(preset.String(), func(t *testing.T) {
			kp := &authfakes.FakeKeyProvider{}
			kp.GetSecretReturns("testkey")
			n, err := webhook.NewDefaultNotifier(webhook.DefaultWebHookConfig, kp)
			require.NoError(t, err)
			launcher := &openviduEgressLauncher{}
			rm := NewRoom(
				&livekit.Room{Name: "room", Sid: "RM_room"},
				&livekit.RoomInternal{ParticipantEgress: &livekit.AutoParticipantEgress{
					Options: &livekit.AutoParticipantEgress_Preset{Preset: preset},
				}},
				WebRTCConfig{},
				config.RoomConfig{EmptyTimeout: 5 * 60, DepartureTimeout: 1},
				&sfu.AudioConfig{},
				&livekit.ServerInfo{Edition: livekit.ServerInfo_Standard, Version: version.Version, Protocol: types.CurrentProtocol},
				telemetry.NewTelemetryService(n, &telemetryfakes.FakeAnalyticsService{}),
				nil, nil, launcher,
			)
			t.Cleanup(func() { rm.Close(types.RoomCloseReasonUnknown) })

			join := func(identity livekit.ParticipantIdentity) *typesfakes.FakeLocalParticipant {
				p := NewMockParticipant(identity, types.CurrentProtocol, false, true, rm.LocalParticipantListener())
				require.NoError(t, rm.Join(p, nil, &ParticipantOptions{AutoSubscribe: true}, iceServersForRoom))
				p.StateReturns(livekit.ParticipantInfo_ACTIVE)
				p.IsReadyReturns(true)
				return p
			}
			// what RoomManager does when a new session of the same identity joins
			replace := func(old *typesfakes.FakeLocalParticipant) *typesfakes.FakeLocalParticipant {
				rm.RemoveParticipant(old.Identity(), old.ID(), types.ParticipantCloseReasonDuplicateIdentity)
				return join(old.Identity())
			}
			publish := func(p *typesfakes.FakeLocalParticipant) {
				rm.LocalParticipantListener().OnTrackPublished(p, NewMockTrack(livekit.TrackType_VIDEO, "camera"))
			}
			// egresses start in goroutines: wait for them, then check no other one follows
			expect := func(identity string, egresses int, msg string) {
				require.Eventually(t, func() bool { return launcher.count(identity) >= egresses }, time.Second,
					10*time.Millisecond, msg)
				time.Sleep(50 * time.Millisecond)
				require.Equal(t, egresses, launcher.count(identity), msg)
			}

			alice := join("alice")
			expect("alice", 0, "joining starts no egress")
			publish(alice)
			publish(alice)
			expect("alice", 1, "the first track starts the egress, and only it")

			alice = replace(alice)
			publish(alice)
			expect("alice", 1, "a full reconnection keeps the egress")

			alice = replace(replace(alice))
			publish(alice)
			expect("alice", 1, "the egress goes on through sessions that never published")

			rm.RemoveParticipant(alice.Identity(), alice.ID(), types.ParticipantCloseReasonClientRequestLeave)
			publish(join("alice"))
			expect("alice", 2, "leaving and joining again starts a new egress")

			bob := replace(join("bob"))
			publish(bob)
			expect("bob", 1, "a session replaced before publishing has no egress to keep")
		})
	}
}

// END OPENVIDU BLOCK
