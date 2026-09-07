// Copyright 2024 OpenVidu
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
	"sync"

	"github.com/livekit/livekit-server/pkg/service"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/livekit-server/openvidu/goutil"
)

type LivekitHelper struct {
	roomStore    *service.ObjectStore
	egressStore  *service.EgressStore
	ingressStore *service.IngressStore
}

var once sync.Once

// Singleton
var livekitHelperInstance *LivekitHelper

func GetInstance() *LivekitHelper {
	if livekitHelperInstance == nil {
		logger.Errorw("LivekitHelper instance is not initiated", nil)
	}
	return livekitHelperInstance
}

func Init(server *service.LivekitServer) {
	if livekitHelperInstance == nil {
		once.Do(
			func() {
				roomStore, err := goutil.GetPrivateStructField[service.ObjectStore](server.RoomManager(), "roomStore")
				if err != nil {
					logger.Errorw("failed to retrieve ServiceStore", err)
					panic(err)
				}
				ioInfoService, err := goutil.GetPrivateStructField[*service.IOInfoService](server, "ioService")
				if err != nil {
					logger.Errorw("failed to retrieve IOInfoService", err)
					panic(err)
				}
				egressStore, err := goutil.GetPrivateStructField[service.EgressStore](ioInfoService, "es")
				if err != nil {
					logger.Errorw("failed to retrieve EgressStore", err)
					panic(err)
				}
				ingressStore, err := goutil.GetPrivateStructField[service.IngressStore](ioInfoService, "is")
				if err != nil {
					logger.Errorw("failed to retrieve IngressStore", err)
					panic(err)
				}
				livekitHelperInstance = &LivekitHelper{
					roomStore:    roomStore,
					egressStore:  egressStore,
					ingressStore: ingressStore,
				}
			})
	}
}

func (o *LivekitHelper) ListActiveRooms() ([]*livekit.Room, error) {
	ctx := context.Background()
	rooms, err := (*o.roomStore).ListRooms(ctx, nil)
	if err != nil {
		return nil, err
	}
	return rooms, nil
}

func (o *LivekitHelper) ListActiveParticipants() ([]*livekit.ParticipantInfo, error) {
	rooms, err := o.ListActiveRooms()
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	participants := make([]*livekit.ParticipantInfo, 0)

	for _, room := range rooms {
		roomParticipants, err := (*o.roomStore).ListParticipants(ctx, livekit.RoomName(room.Name))
		if err != nil {
			return nil, err
		}

		participants = append(participants, roomParticipants...)
	}

	return participants, nil
}

func (o *LivekitHelper) ListActiveEgresses() ([]*livekit.EgressInfo, error) {
	ctx := context.Background()
	egresses, err := (*o.egressStore).ListEgress(ctx, "", true)
	if err != nil {
		return nil, err
	}
	return egresses, nil
}

func (o *LivekitHelper) ListActiveIngresses() ([]*livekit.IngressInfo, error) {
	ctx := context.Background()
	ingresses, err := (*o.ingressStore).ListIngress(ctx, "")
	if err != nil {
		return nil, err
	}

	activeIngresses := make([]*livekit.IngressInfo, 0)
	for _, ingress := range ingresses {
		if ingress.State == nil {
			continue
		}
		if ingress.State.Status == livekit.IngressState_ENDPOINT_PUBLISHING && ingress.State.ResourceId != "" {
			activeIngresses = append(activeIngresses, ingress)
		}
	}

	return activeIngresses, nil
}
