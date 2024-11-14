package livekithelper

import (
	"context"
	"sync"

	"github.com/livekit/livekit-server/pkg/service"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/openvidu/openvidu-livekit/openvidu/goutil"
)

type LivekitHelper struct {
	roomStore    *service.ServiceStore
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
				roomStore, err := goutil.GetPrivateStructField[service.ServiceStore](server.RoomManager(), "roomStore")
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

func (o *LivekitHelper) ListActiveParticipants(roomName livekit.RoomName) ([]*livekit.ParticipantInfo, error) {
	ctx := context.Background()
	participants, err := (*o.roomStore).ListParticipants(ctx, roomName)
	if err != nil {
		return nil, err
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
	return ingresses, nil
}
