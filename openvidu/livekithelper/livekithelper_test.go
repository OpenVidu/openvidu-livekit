// Copyright 2026 OpenVidu
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

package livekithelper

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"

	"github.com/livekit/livekit-server/openvidu/livekithelper/livekithelperinterface"
	"github.com/livekit/livekit-server/pkg/service"
)

var _ livekithelperinterface.LivekitHelper = (*LivekitHelper)(nil)

type fakeRegistry struct {
	nodes []*livekit.Node
	err   error
}

func (r fakeRegistry) ListNodes() ([]*livekit.Node, error) {
	return r.nodes, r.err
}

func TestListParticipants(t *testing.T) {
	var store service.ObjectStore = service.NewLocalStore()
	ctx := context.Background()
	require.NoError(t, store.StoreRoom(ctx, &livekit.Room{Sid: "RM_a", Name: "a"}, nil))
	require.NoError(t, store.StoreParticipant(ctx, "a", &livekit.ParticipantInfo{Sid: "PA_1", Identity: "one"}))
	require.NoError(t, store.StoreParticipant(ctx, "a", &livekit.ParticipantInfo{Sid: "PA_2", Identity: "two"}))
	helper := &LivekitHelper{roomStore: &store}

	participants, err := helper.ListParticipants("a")
	require.NoError(t, err)
	sids := []string{}
	for _, participant := range participants {
		sids = append(sids, participant.GetSid())
	}
	require.ElementsMatch(t, []string{"PA_1", "PA_2"}, sids)

	participants, err = helper.ListParticipants("missing")
	require.NoError(t, err)
	require.Empty(t, participants)
}

func TestIsNodeAvailable(t *testing.T) {
	now := time.Now().Unix()
	helper := &LivekitHelper{nodes: fakeRegistry{nodes: []*livekit.Node{
		{Id: "ND_fresh", Stats: &livekit.NodeStats{UpdatedAt: now}},
		{Id: "ND_stale", Stats: &livekit.NodeStats{UpdatedAt: now - 120}},
		{Id: "ND_starting"},
	}}}
	for node, available := range map[string]bool{"ND_fresh": true, "ND_stale": false, "ND_starting": true, "ND_gone": false} {
		got, err := helper.IsNodeAvailable(node)
		require.NoError(t, err)
		require.Equal(t, available, got, node)
	}

	broken := &LivekitHelper{nodes: fakeRegistry{err: errors.New("redis down")}}
	_, err := broken.IsNodeAvailable("ND_fresh")
	require.Error(t, err)
}
