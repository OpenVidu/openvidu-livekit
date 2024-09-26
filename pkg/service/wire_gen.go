// Code generated by Wire. DO NOT EDIT.

//go:generate go run -mod=mod github.com/google/wire/cmd/wire
//go:build !wireinject
// +build !wireinject

package service

import (
	"fmt"
	"github.com/livekit/livekit-server/pkg/agent"
	"github.com/livekit/livekit-server/pkg/clientconfiguration"
	"github.com/livekit/livekit-server/pkg/config"
	"github.com/livekit/livekit-server/pkg/routing"
	"github.com/livekit/livekit-server/pkg/sfu"
	"github.com/livekit/livekit-server/pkg/telemetry"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	redis2 "github.com/livekit/protocol/redis"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/utils"
	"github.com/livekit/protocol/webhook"
	"github.com/livekit/psrpc"
	"github.com/pion/turn/v2"
	"github.com/pkg/errors"
	"github.com/redis/go-redis/v9"
	"gopkg.in/yaml.v3"
	"os"
)

import (
	_ "net/http/pprof"
)

// Injectors from wire.go:

func InitializeServer(conf *config.Config, currentNode routing.LocalNode) (*LivekitServer, error) {
	limitConfig := getLimitConf(conf)
	apiConfig := config.DefaultAPIConfig()
	psrpcConfig := getPSRPCConfig(conf)
	universalClient, err := createRedisClient(conf)
	if err != nil {
		return nil, err
	}
	nodeID := getNodeID(currentNode)
	messageBus := getMessageBus(universalClient)
	signalRelayConfig := getSignalRelayConfig(conf)
	signalClient, err := routing.NewSignalClient(nodeID, messageBus, signalRelayConfig)
	if err != nil {
		return nil, err
	}
	clientParams := getPSRPCClientParams(psrpcConfig, messageBus)
	keepalivePubSub, err := rpc.NewKeepalivePubSub(clientParams)
	if err != nil {
		return nil, err
	}
	router := routing.CreateRouter(universalClient, currentNode, signalClient, keepalivePubSub)
	objectStore := createStore(universalClient)
	roomAllocator, err := NewRoomAllocator(conf, router, objectStore)
	if err != nil {
		return nil, err
	}
	client, err := agent.NewAgentClient(messageBus)
	if err != nil {
		return nil, err
	}
	egressClient, err := rpc.NewEgressClient(clientParams)
	if err != nil {
		return nil, err
	}
	egressStore := getEgressStore(objectStore)
	ingressStore := getIngressStore(objectStore)
	sipStore := getSIPStore(objectStore)
	keyProvider, err := createKeyProvider(conf)
	if err != nil {
		return nil, err
	}
	queuedNotifier, err := createWebhookNotifier(conf, keyProvider)
	if err != nil {
		return nil, err
	}
	analyticsService := telemetry.NewAnalyticsService(conf, currentNode)
	telemetryService := telemetry.NewTelemetryService(queuedNotifier, analyticsService)
	ioInfoService, err := NewIOInfoService(messageBus, egressStore, ingressStore, sipStore, telemetryService)
	if err != nil {
		return nil, err
	}
	rtcEgressLauncher := NewEgressLauncher(egressClient, ioInfoService)
	topicFormatter := rpc.NewTopicFormatter()
	roomClient, err := rpc.NewTypedRoomClient(clientParams)
	if err != nil {
		return nil, err
	}
	participantClient, err := rpc.NewTypedParticipantClient(clientParams)
	if err != nil {
		return nil, err
	}
	roomService, err := NewRoomService(limitConfig, apiConfig, psrpcConfig, router, roomAllocator, objectStore, client, rtcEgressLauncher, topicFormatter, roomClient, participantClient)
	if err != nil {
		return nil, err
	}
	agentDispatchInternalClient, err := rpc.NewTypedAgentDispatchInternalClient(clientParams)
	if err != nil {
		return nil, err
	}
	agentDispatchService := NewAgentDispatchService(agentDispatchInternalClient, topicFormatter)
	egressService := NewEgressService(egressClient, rtcEgressLauncher, objectStore, ioInfoService, roomService)
	ingressConfig := getIngressConfig(conf)
	ingressClient, err := rpc.NewIngressClient(clientParams)
	if err != nil {
		return nil, err
	}
	ingressService := NewIngressService(ingressConfig, nodeID, messageBus, ingressClient, ingressStore, ioInfoService, telemetryService)
	sipConfig := getSIPConfig(conf)
	sipClient, err := rpc.NewSIPClient(messageBus)
	if err != nil {
		return nil, err
	}
	sipService := NewSIPService(sipConfig, nodeID, messageBus, sipClient, sipStore, roomService, telemetryService)
	rtcService := NewRTCService(conf, roomAllocator, objectStore, router, currentNode, client, telemetryService)
	agentService, err := NewAgentService(conf, currentNode, messageBus, keyProvider)
	if err != nil {
		return nil, err
	}
	clientConfigurationManager := createClientConfiguration()
	agentStore := getAgentStore(objectStore)
	timedVersionGenerator := utils.NewDefaultTimedVersionGenerator()
	turnAuthHandler := NewTURNAuthHandler(keyProvider)
	forwardStats := createForwardStats(conf)
	roomManager, err := NewLocalRoomManager(conf, objectStore, currentNode, router, telemetryService, clientConfigurationManager, client, agentStore, rtcEgressLauncher, timedVersionGenerator, turnAuthHandler, messageBus, forwardStats)
	if err != nil {
		return nil, err
	}
	signalServer, err := NewDefaultSignalServer(currentNode, messageBus, signalRelayConfig, router, roomManager)
	if err != nil {
		return nil, err
	}
	authHandler := getTURNAuthHandlerFunc(turnAuthHandler)
	server, err := newInProcessTurnServer(conf, authHandler)
	if err != nil {
		return nil, err
	}
	livekitServer, err := NewLivekitServer(conf, roomService, agentDispatchService, egressService, ingressService, sipService, ioInfoService, rtcService, agentService, keyProvider, router, roomManager, signalServer, server, currentNode)
	if err != nil {
		return nil, err
	}
	return livekitServer, nil
}

func InitializeRouter(conf *config.Config, currentNode routing.LocalNode) (routing.Router, error) {
	universalClient, err := createRedisClient(conf)
	if err != nil {
		return nil, err
	}
	nodeID := getNodeID(currentNode)
	messageBus := getMessageBus(universalClient)
	signalRelayConfig := getSignalRelayConfig(conf)
	signalClient, err := routing.NewSignalClient(nodeID, messageBus, signalRelayConfig)
	if err != nil {
		return nil, err
	}
	psrpcConfig := getPSRPCConfig(conf)
	clientParams := getPSRPCClientParams(psrpcConfig, messageBus)
	keepalivePubSub, err := rpc.NewKeepalivePubSub(clientParams)
	if err != nil {
		return nil, err
	}
	router := routing.CreateRouter(universalClient, currentNode, signalClient, keepalivePubSub)
	return router, nil
}

// wire.go:

func getNodeID(currentNode routing.LocalNode) livekit.NodeID {
	return livekit.NodeID(currentNode.Id)
}

func createKeyProvider(conf *config.Config) (auth.KeyProvider, error) {

	if conf.KeyFile != "" {
		var otherFilter os.FileMode = 0007
		if st, err := os.Stat(conf.KeyFile); err != nil {
			return nil, err
		} else if st.Mode().Perm()&otherFilter != 0000 {
			return nil, fmt.Errorf("key file others permissions must be set to 0")
		}
		f, err := os.Open(conf.KeyFile)
		if err != nil {
			return nil, err
		}
		defer func() {
			_ = f.Close()
		}()
		decoder := yaml.NewDecoder(f)
		if err = decoder.Decode(conf.Keys); err != nil {
			return nil, err
		}
	}

	if len(conf.Keys) == 0 {
		return nil, errors.New("one of key-file or keys must be provided in order to support a secure installation")
	}

	return auth.NewFileBasedKeyProviderFromMap(conf.Keys), nil
}

func createWebhookNotifier(conf *config.Config, provider auth.KeyProvider) (webhook.QueuedNotifier, error) {
	wc := conf.WebHook
	if len(wc.URLs) == 0 {
		return nil, nil
	}
	secret := provider.GetSecret(wc.APIKey)
	if secret == "" {
		return nil, ErrWebHookMissingAPIKey
	}

	return webhook.NewDefaultNotifier(wc.APIKey, secret, wc.URLs), nil
}

func createRedisClient(conf *config.Config) (redis.UniversalClient, error) {
	if !conf.Redis.IsConfigured() {
		return nil, nil
	}
	return redis2.GetRedisClient(&conf.Redis)
}

func createStore(rc redis.UniversalClient) ObjectStore {
	if rc != nil {
		return NewRedisStore(rc)
	}
	return NewLocalStore()
}

func getMessageBus(rc redis.UniversalClient) psrpc.MessageBus {
	if rc == nil {
		return psrpc.NewLocalMessageBus()
	}
	return psrpc.NewRedisMessageBus(rc)
}

func getEgressStore(s ObjectStore) EgressStore {
	switch store := s.(type) {
	case *RedisStore:
		return store
	default:
		return nil
	}
}

func getIngressStore(s ObjectStore) IngressStore {
	switch store := s.(type) {
	case *RedisStore:
		return store
	default:
		return nil
	}
}

func getAgentStore(s ObjectStore) AgentStore {
	switch store := s.(type) {
	case *RedisStore:
		return store
	case *LocalStore:
		return store
	default:
		return nil
	}
}

func getIngressConfig(conf *config.Config) *config.IngressConfig {
	return &conf.Ingress
}

func getSIPStore(s ObjectStore) SIPStore {
	switch store := s.(type) {
	case *RedisStore:
		return store
	default:
		return nil
	}
}

func getSIPConfig(conf *config.Config) *config.SIPConfig {
	return &conf.SIP
}

func createClientConfiguration() clientconfiguration.ClientConfigurationManager {
	return clientconfiguration.NewStaticClientConfigurationManager(clientconfiguration.StaticConfigurations)
}

func getLimitConf(config2 *config.Config) config.LimitConfig {
	return config2.Limit
}

func getSignalRelayConfig(config2 *config.Config) config.SignalRelayConfig {
	return config2.SignalRelay
}

func getPSRPCConfig(config2 *config.Config) rpc.PSRPCConfig {
	return config2.PSRPC
}

func getPSRPCClientParams(config2 rpc.PSRPCConfig, bus psrpc.MessageBus) rpc.ClientParams {
	return rpc.NewClientParams(config2, bus, logger.GetLogger(), rpc.PSRPCMetricsObserver{})
}

func createForwardStats(conf *config.Config) *sfu.ForwardStats {
	if conf.RTC.ForwardStats.SummaryInterval == 0 || conf.RTC.ForwardStats.ReportInterval == 0 || conf.RTC.ForwardStats.ReportWindow == 0 {
		return nil
	}
	return sfu.NewForwardStats(conf.RTC.ForwardStats.SummaryInterval, conf.RTC.ForwardStats.ReportInterval, conf.RTC.ForwardStats.ReportWindow)
}

func newInProcessTurnServer(conf *config.Config, authHandler turn.AuthHandler) (*turn.Server, error) {
	return NewTurnServer(conf, authHandler, false)
}
