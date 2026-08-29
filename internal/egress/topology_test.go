package egress_test

import (
	"testing"

	"github.com/jd316/TortureU/internal/egress"
)

// spec: R-DC2-3
func TestBuildTopologyMarksSUTNetworkInternalWithNoRouteOut(t *testing.T) {
	top := egress.BuildTopology("sut-net", "egress-net", "tortureu-proxy")

	net, ok := top.Networks["sut-net"]
	if !ok {
		t.Fatalf("BuildTopology: no network entry for the SUT network")
	}
	if !net.Internal {
		t.Errorf("sut-net.internal = false, want true — a non-internal network has a default route out, which defeats R-DC2-3's topological guarantee")
	}
}

// spec: R-DC2-3
func TestBuildTopologyDualHomesOnlyTheProxy(t *testing.T) {
	top := egress.BuildTopology("sut-net", "egress-net", "tortureu-proxy")

	proxy, ok := top.Services["tortureu-proxy"]
	if !ok {
		t.Fatalf("BuildTopology: no service entry for the proxy")
	}
	has := func(name string) bool {
		for _, n := range proxy.Networks {
			if n == name {
				return true
			}
		}
		return false
	}
	if !has("sut-net") || !has("egress-net") {
		t.Errorf("proxy networks = %v, want both sut-net and egress-net — the proxy must be the SUT's only path off-box", proxy.Networks)
	}
}
