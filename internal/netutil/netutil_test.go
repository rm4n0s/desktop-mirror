package netutil_test

import (
	"net"
	"testing"

	"github.com/rm4n0s/desktop-mirror/internal/netutil"
)

func TestRank(t *testing.T) {
	in := []netutil.Iface{
		{Name: "docker0", IP: net.ParseIP("172.17.0.1")},
		{Name: "wwan0", IP: net.ParseIP("100.64.3.4")},
		{Name: "wlan0", IP: net.ParseIP("192.168.1.20")},
		{Name: "lo", IP: net.ParseIP("127.0.0.1")},
		{Name: "eth0", IP: net.ParseIP("169.254.10.1")},
		{Name: "eth1", IP: net.ParseIP("fe80::1")},
		{Name: "enp3s0", IP: net.ParseIP("10.0.0.7")},
		{Name: "virbr0", IP: net.ParseIP("192.168.122.1")},
	}
	want := []string{"wlan0", "enp3s0", "wwan0", "docker0", "virbr0"}

	got := netutil.Rank(in)
	if len(got) != len(want) {
		t.Fatalf("got %v, want names %v", got, want)
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Errorf("rank[%d] = %s, want %s (all: %v)", i, got[i].Name, name, got)
		}
	}
}

func TestRankEmpty(t *testing.T) {
	if got := netutil.Rank(nil); len(got) != 0 {
		t.Errorf("Rank(nil) = %v", got)
	}
}

func TestIfaceString(t *testing.T) {
	got := netutil.Iface{Name: "wlan0", IP: net.ParseIP("192.168.1.20")}.String()
	if got != "wlan0 (192.168.1.20)" {
		t.Errorf("String() = %q", got)
	}
}
