package service

import (
	"net"
	"testing"
	"time"

	"github.com/livekit/mediatransportutil/pkg/rtcconfig"
	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"

	"github.com/livekit/livekit-server/pkg/config"
	"github.com/livekit/livekit-server/pkg/routing"
)

func TestStopBeforeStartStopsTheServer(t *testing.T) {
	conf, err := config.NewConfig("", true, nil, nil)
	require.NoError(t, err)
	conf.Keys = map[string]string{"key": "secret-of-at-least-32-characters-long"}
	conf.BindAddresses = []string{"127.0.0.1"}
	conf.Port = uint32(freePort(t))
	conf.RTC.TCPPort = uint32(freePort(t))
	conf.RTC.UDPPort = rtcconfig.PortRange{Start: freePort(t)}
	node, err := routing.NewLocalNode(conf)
	require.NoError(t, err)
	server, err := InitializeServer(conf, node)
	require.NoError(t, err)

	stopped := make(chan struct{})
	go func() {
		server.Stop(true)
		close(stopped)
	}()
	require.Eventually(t, func() bool { return server.Node().State == livekit.NodeState_SHUTTING_DOWN },
		time.Second, time.Millisecond)

	started := make(chan error, 1)
	go func() { started <- server.Start() }()
	select {
	case err := <-started:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not stop for the Stop requested before it")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not return after Start")
	}
	require.False(t, server.IsRunning())
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}
