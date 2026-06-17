package client_test

import (
	"testing"

	"github.com/velonetics/lura/v2/config"
	"github.com/velonetics/lura/v2/logging"
	"github.com/velonetics/lura/v2/proxy"
	maingrpc "github.com/velonetics/velonetics-grpc/v2"
	"github.com/velonetics/velonetics-grpc/v2/catalog"
	"github.com/velonetics/velonetics-grpc/v2/client"
)

func TestBackendFactoryRequiresCatalog(t *testing.T) {
	maingrpc.SetGlobalRegistry(nil)
	remote := &config.Backend{
		Host:       []string{"localhost:4242"},
		URLPattern: "/flight_finder.Flights/FindFlight",
		ExtraConfig: config.ExtraConfig{
			client.Namespace: map[string]interface{}{},
		},
	}
	called := false
	bf := client.BackendFactory(logging.NoOp, func(remote *config.Backend) proxy.Proxy {
		called = true
		return nil
	})
	_ = bf(remote)
	if !called {
		t.Fatal("expected fallback when catalog missing")
	}
}

func TestBackendFactoryBuildsProxy(t *testing.T) {
	reg := catalog.NewRegistry()
	if err := reg.Load([]string{"../testdata/contracts/flights.pb"}, logging.NoOp); err != nil {
		t.Fatal(err)
	}
	maingrpc.SetGlobalRegistry(reg)
	remote := &config.Backend{
		Host:       []string{"localhost:4242"},
		URLPattern: "/flight_finder.Flights/FindFlight",
		ExtraConfig: config.ExtraConfig{
			client.Namespace: map[string]interface{}{},
		},
	}
	bf := client.BackendFactory(logging.NoOp, func(remote *config.Backend) proxy.Proxy {
		t.Fatal("should not fallback")
		return nil
	})
	if bf(remote) == nil {
		t.Fatal("expected grpc proxy")
	}
}
