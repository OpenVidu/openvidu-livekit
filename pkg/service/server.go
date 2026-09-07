// Copyright 2023 LiveKit, Inc.
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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	httppprof "net/http/pprof"
	"runtime"
	"runtime/pprof"
	"strconv"
	"time"

	"github.com/openvidu/openvidu-livekit/pkg/routing/selector"
	"github.com/pion/turn/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/cors"
	"github.com/twitchtv/twirp"
	"github.com/urfave/negroni/v3"
	"go.uber.org/atomic"
	"golang.org/x/sync/errgroup"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils/xtwirp"

	"github.com/livekit/livekit-server/pkg/config"
	"github.com/livekit/livekit-server/pkg/routing"
	"github.com/livekit/livekit-server/version"
	"github.com/openvidu/openvidu-livekit/openvidu/openviduversion"
)

type LivekitServer struct {
	config       *config.Config
	ioService    *IOInfoService
	rtcService   *RTCService
	whipService  *WHIPService
	agentService *AgentService
	httpServer   *http.Server
	promServer   *http.Server
	debugServer  *http.Server
	router       routing.Router
	roomManager  *RoomManager
	signalServer *SignalServer
	turnServer   *turn.Server
	currentNode  routing.LocalNode
	running      atomic.Bool
	doneChan     chan struct{}
	closedChan   chan struct{}
}

func NewLivekitServer(conf *config.Config,
	roomService livekit.RoomService,
	agentDispatchService *AgentDispatchService,
	egressService *EgressService,
	ingressService *IngressService,
	sipService *SIPService,
	ioService *IOInfoService,
	rtcService *RTCService,
	whipService *WHIPService,
	agentService *AgentService,
	keyProvider auth.KeyProvider,
	router routing.Router,
	roomManager *RoomManager,
	signalServer *SignalServer,
	turnServer *turn.Server,
	currentNode routing.LocalNode,
) (s *LivekitServer, err error) {
	s = &LivekitServer{
		config:       conf,
		ioService:    ioService,
		rtcService:   rtcService,
		whipService:  whipService,
		agentService: agentService,
		router:       router,
		roomManager:  roomManager,
		signalServer: signalServer,
		// turn server starts automatically
		turnServer:  turnServer,
		currentNode: currentNode,
		closedChan:  make(chan struct{}),
	}

	middlewares := []negroni.Handler{
		// always first
		negroni.NewRecovery(),
		// CORS is allowed, we rely on token authentication to prevent improper use
		cors.New(cors.Options{
			AllowOriginFunc: func(origin string) bool {
				return true
			},
			AllowedMethods: []string{"OPTIONS", "HEAD", "GET", "POST", "PATCH", "DELETE"},
			AllowedHeaders: []string{"*"},
			ExposedHeaders: []string{"*"},
			// allow preflight to be cached for a day
			MaxAge: 86400,
		}),
		negroni.HandlerFunc(RemoveDoubleSlashes),
		// limit request body size so large messages cannot exhaust memory
		NewRequestBodyLimiter(conf.Limit.MaxAPIRequestBodySize),
	}
	if keyProvider != nil {
		middlewares = append(middlewares, NewAPIKeyAuthMiddleware(keyProvider))
	}

	serverOptions := []any{
		twirp.WithServerHooks(twirp.ChainHooks(
			TwirpLogger(),
			TwirpEgressID(),
			TwirpRequestStatusReporter(),
		)),
	}
	for _, opt := range xtwirp.DefaultServerOptions() {
		serverOptions = append(serverOptions, opt)
	}
	roomServer := livekit.NewRoomServiceServer(roomService, serverOptions...)
	agentDispatchServer := livekit.NewAgentDispatchServiceServer(agentDispatchService, serverOptions...)
	egressServer := livekit.NewEgressServer(egressService, serverOptions...)
	ingressServer := livekit.NewIngressServer(ingressService, serverOptions...)
	sipServer := livekit.NewSIPServer(sipService, serverOptions...)

	mux := http.NewServeMux()
	if conf.Development {
		// pprof handlers are registered onto DefaultServeMux
		mux = http.DefaultServeMux
		mux.HandleFunc("/debug/goroutine", s.debugGoroutines)
		mux.HandleFunc("/debug/rooms", s.debugInfo)
	}

	// BEGIN OPENVIDU BLOCK
	mux.HandleFunc("/twirp/health", s.healthCheck)
	mux.HandleFunc("/twirp/debug", s.debugRooms)
	mux.HandleFunc("/twirp/info", s.gatherInfo)
	// END OPENVIDU BLOCK

	xtwirp.RegisterServer(mux, roomServer)
	xtwirp.RegisterServer(mux, agentDispatchServer)
	xtwirp.RegisterServer(mux, egressServer)
	xtwirp.RegisterServer(mux, ingressServer)
	xtwirp.RegisterServer(mux, sipServer)
	rtcService.SetupRoutes(mux)
	whipService.SetupRoutes(mux)
	mux.Handle("/agent", agentService)
	mux.HandleFunc("/", s.defaultHandler)

	s.httpServer = &http.Server{
		Handler: configureMiddlewares(mux, middlewares...),
	}

	if conf.PrometheusPort > 0 {
		logger.Warnw("prometheus_port is deprecated, please switch to prometheus.port instead", nil)
		conf.Prometheus.Port = conf.PrometheusPort
	}

	if conf.Prometheus.Port > 0 {
		promHandler := promhttp.Handler()
		if conf.Prometheus.Username != "" && conf.Prometheus.Password != "" {
			protectedHandler := negroni.New()
			protectedHandler.Use(negroni.HandlerFunc(GenBasicAuthMiddleware(conf.Prometheus.Username, conf.Prometheus.Password)))
			protectedHandler.UseHandler(promHandler)
			promHandler = protectedHandler
		} else if conf.Prometheus.Username != "" || conf.Prometheus.Password != "" {
			logger.Warnw("prometheus username or password is set but not both, set both or nothing for unauthenticated access", nil)
			err = errors.New("prometheus username or password is set but not both, set both or nothing for unauthenticated access")
			return
		}
		s.promServer = &http.Server{
			Handler: promHandler,
		}
	}

	if conf.DebugHandler.Port > 0 {
		debugMux := http.NewServeMux()
		debugMux.HandleFunc("/debug/pprof/", httppprof.Index)
		debugMux.HandleFunc("/debug/pprof/cmdline", httppprof.Cmdline)
		debugMux.HandleFunc("/debug/pprof/profile", httppprof.Profile)
		debugMux.HandleFunc("/debug/pprof/symbol", httppprof.Symbol)
		debugMux.HandleFunc("/debug/pprof/trace", httppprof.Trace)
		debugMux.HandleFunc("/debug/goroutine", s.debugGoroutines)
		debugMux.HandleFunc("/debug/rooms", s.debugInfo)
		s.debugServer = &http.Server{
			Handler: http.Handler(debugMux),
		}
	}

	// BEGIN OPENVIDU BLOCK
	// Clean dead nodes after the AvailableSeconds time has elapsed
	// This ensures that a restarted node will always autoclean itself
	time.AfterFunc(time.Second*(selector.AvailableSeconds+1), func() {
		if err = router.RemoveDeadNodes(roomManager); err != nil {
			logger.Errorw("could not remove dead nodes at first attempt", err)
		}
	})
	// Gouroutine that every 3 minutes cleans up the Redis database from dead nodes and all associated entities
	ticker := time.NewTicker(time.Minute * 3)
	go func(ticker *time.Ticker) {
		for range ticker.C {
			logger.Debugw("cleaning up dead nodes")
			if err := router.RemoveDeadNodes(roomManager); err != nil {
				logger.Errorw("could not remove dead nodes", err)
			}
		}
	}(ticker)

	if err = router.RemoveDeadNodes(roomManager); err != nil {
		return
	}
	// END OPENVIDU BLOCK

	return
}

func (s *LivekitServer) Node() *livekit.Node {
	return s.currentNode.Clone()
}

func (s *LivekitServer) HTTPPort() int {
	return int(s.config.Port)
}

func (s *LivekitServer) IsRunning() bool {
	return s.running.Load()
}

func (s *LivekitServer) Start() error {
	if s.running.Load() {
		return errors.New("already running")
	}
	s.doneChan = make(chan struct{})

	if err := s.router.RegisterNode(); err != nil {
		return err
	}
	defer func() {
		if err := s.router.UnregisterNode(); err != nil {
			logger.Errorw("could not unregister node", err)
		}
	}()

	if err := s.router.Start(); err != nil {
		return err
	}

	if err := s.ioService.Start(); err != nil {
		return err
	}

	addresses := s.config.BindAddresses
	if addresses == nil {
		addresses = []string{""}
	}

	// ensure we could listen
	listeners := make([]net.Listener, 0)
	promListeners := make([]net.Listener, 0)
	debugListeners := make([]net.Listener, 0)
	for _, addr := range addresses {
		ln, err := net.Listen("tcp", net.JoinHostPort(addr, strconv.Itoa(int(s.config.Port))))
		if err != nil {
			return err
		}
		listeners = append(listeners, ln)

		if s.promServer != nil {
			ln, err = net.Listen("tcp", net.JoinHostPort(addr, strconv.Itoa(int(s.config.Prometheus.Port))))
			if err != nil {
				return err
			}
			promListeners = append(promListeners, ln)
		}

		if s.debugServer != nil {
			ln, err = net.Listen("tcp", net.JoinHostPort(addr, strconv.Itoa(int(s.config.DebugHandler.Port))))
			if err != nil {
				return err
			}
			debugListeners = append(debugListeners, ln)
		}
	}

	values := []any{
		"portHttp", s.config.Port,
		"nodeID", s.currentNode.NodeID(),
		"nodeIP", s.currentNode.NodeIP(),
		"version", version.Version,
	}
	if s.config.BindAddresses != nil {
		values = append(values, "bindAddresses", s.config.BindAddresses)
	}
	if s.config.RTC.TCPPort != 0 {
		values = append(values, "rtc.portTCP", s.config.RTC.TCPPort)
	}
	if !s.config.RTC.ForceTCP && s.config.RTC.UDPPort.Valid() {
		values = append(values, "rtc.portUDP", s.config.RTC.UDPPort)
	} else {
		values = append(values,
			"rtc.portICERange", []uint32{s.config.RTC.ICEPortRangeStart, s.config.RTC.ICEPortRangeEnd},
		)
	}
	if s.config.Prometheus.Port != 0 {
		values = append(values, "portPrometheus", s.config.Prometheus.Port)
	}
	if s.config.DebugHandler.Port != 0 {
		values = append(values, "portDebugHandler", s.config.DebugHandler.Port)
	}
	if s.config.Region != "" {
		values = append(values, "region", s.config.Region)
	}
	logger.Infow("starting LiveKit server", values...)
	if runtime.GOOS == "windows" {
		logger.Infow("Windows detected, capacity management is unavailable")
	}

	for _, promLn := range promListeners {
		go s.promServer.Serve(promLn)
	}

	for _, debugLn := range debugListeners {
		go s.debugServer.Serve(debugLn)
	}

	if err := s.signalServer.Start(); err != nil {
		return err
	}

	httpGroup := &errgroup.Group{}
	for _, ln := range listeners {
		l := ln
		httpGroup.Go(func() error {
			return s.httpServer.Serve(l)
		})
	}
	go func() {
		if err := httpGroup.Wait(); err != http.ErrServerClosed {
			logger.Errorw("could not start server", err)
			s.Stop(true)
		}
	}()

	go s.backgroundWorker()

	// give time for Serve goroutine to start
	time.Sleep(100 * time.Millisecond)

	s.running.Store(true)

	<-s.doneChan

	// wait for shutdown
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	_ = s.httpServer.Shutdown(ctx)
	if s.debugServer != nil {
		_ = s.debugServer.Shutdown(ctx)
	}

	if s.turnServer != nil {
		_ = s.turnServer.Close()
	}

	s.roomManager.Stop()
	s.signalServer.Stop()
	s.ioService.Stop()

	close(s.closedChan)
	return nil
}

func (s *LivekitServer) Stop(force bool) {
	// wait for all participants to exit
	s.router.Drain()
	partTicker := time.NewTicker(5 * time.Second)
	waitingForParticipants := !force && s.roomManager.HasParticipants()
	for waitingForParticipants {
		<-partTicker.C
		logger.Infow("waiting for participants to exit")
		waitingForParticipants = s.roomManager.HasParticipants()
	}
	partTicker.Stop()

	if !s.running.Swap(false) {
		return
	}

	s.router.Stop()
	close(s.doneChan)

	// wait for fully closed
	<-s.closedChan
}

func (s *LivekitServer) RoomManager() *RoomManager {
	return s.roomManager
}

func (s *LivekitServer) debugGoroutines(w http.ResponseWriter, _ *http.Request) {
	_ = pprof.Lookup("goroutine").WriteTo(w, 2)
}

func (s *LivekitServer) debugInfo(w http.ResponseWriter, _ *http.Request) {
	s.roomManager.lock.RLock()
	info := make([]map[string]any, 0, len(s.roomManager.rooms))
	for _, room := range s.roomManager.rooms {
		info = append(info, room.DebugInfo())
	}
	s.roomManager.lock.RUnlock()

	b, err := json.Marshal(info)
	if err != nil {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(err.Error()))
	} else {
		_, _ = w.Write(b)
	}
}

// BEGIN OPENVIDU BLOCK
type debugRoomsResponse struct {
	Nodes []*livekit.Node   `json:"nodes"`
	Rooms map[string]string `json:"rooms"`
}

// return debug information about rooms, including which node they are on.
// requires a LiveKit token with "roomList" permission
func (s *LivekitServer) debugRooms(w http.ResponseWriter, r *http.Request) {
	if err := EnsureListPermission(r.Context()); err != nil {
		HandleError(w, r, http.StatusUnauthorized, twirpAuthError(err))
		return
	}

	redisRouter, ok := s.router.(*routing.RedisRouter)
	if !ok {
		node := s.Node()
		s.roomManager.lock.RLock()
		rooms := make(map[string]string, len(s.roomManager.rooms))
		for name := range s.roomManager.rooms {
			rooms[string(name)] = node.Id
		}
		s.roomManager.lock.RUnlock()
		s.writeDebugJSON(w, debugRoomsResponse{Nodes: []*livekit.Node{node}, Rooms: rooms})
		return
	}

	nodes, err := redisRouter.ListNodes()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(err.Error()))
		return
	}

	roomNodeMap, err := redisRouter.GetRoomNodeMap()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(err.Error()))
		return
	}

	s.writeDebugJSON(w, debugRoomsResponse{Nodes: nodes, Rooms: roomNodeMap})
}

func (s *LivekitServer) writeDebugJSON(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(err.Error()))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

// return server information
func (s *LivekitServer) gatherInfo(w http.ResponseWriter, r *http.Request) {
	if err := EnsureListPermission(r.Context()); err != nil {
		HandleError(w, r, http.StatusUnauthorized, twirpAuthError(err))
		return
	}

	s.writeDebugJSON(w, openviduversion.ServerInfo)
}

// END OPENVIDU BLOCK

func (s *LivekitServer) defaultHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		s.healthCheck(w, r)
	} else {
		http.NotFound(w, r)
	}
}

func (s *LivekitServer) healthCheck(w http.ResponseWriter, _ *http.Request) {
	var updatedAt time.Time
	if s.Node().Stats != nil {
		updatedAt = time.Unix(s.Node().Stats.UpdatedAt, 0)
	}
	if time.Since(updatedAt) > 4*time.Second {
		w.WriteHeader(http.StatusNotAcceptable)
		_, _ = fmt.Fprintf(w, "Not Ready\nNode Updated At %s", updatedAt)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

// worker to perform periodic tasks per node
func (s *LivekitServer) backgroundWorker() {
	roomTicker := time.NewTicker(1 * time.Second)
	defer roomTicker.Stop()
	for {
		select {
		case <-s.doneChan:
			return
		case <-roomTicker.C:
			s.roomManager.CloseIdleRooms()
		}
	}
}

func configureMiddlewares(handler http.Handler, middlewares ...negroni.Handler) *negroni.Negroni {
	n := negroni.New()
	for _, m := range middlewares {
		n.Use(m)
	}
	n.UseHandler(handler)
	return n
}
