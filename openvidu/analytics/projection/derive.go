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

package projection

import (
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/livekit/livekit-server/openvidu/analytics/schema"
)

// deriveStage returns the last stage of every merged update of a collection: counters from id sets, defaults of
// required fields, durations and expiresAt. It is nil for collections without derived fields.
func deriveStage(collection string, opts Options) bson.D {
	var f *fields
	switch collection {
	case schema.CollectionRooms:
		f = deriveRoom(opts)
	case schema.CollectionParticipants:
		f = deriveParticipant(opts)
	case schema.CollectionTracks:
		f = deriveTrack(opts)
	case schema.CollectionTrackSubscriptions:
		f = deriveSubscription(opts)
	case schema.CollectionEgresses:
		f = deriveEgress(opts)
	case schema.CollectionIngresses:
		f = deriveIngress(opts)
	case schema.CollectionIngressSessions:
		f = deriveIngressSession(opts)
	default:
		return nil
	}
	return stage(f.doc())
}
