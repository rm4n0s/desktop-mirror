// Package netutil discovers the LAN addresses a phone could reach.
package netutil

import (
	"net"
	"slices"
	"strings"

	"github.com/rm4n0s/errors"
)

// Iface is a usable LAN interface with its IPv4 address.
type Iface struct {
	Name string
	IP   net.IP
}

// String returns a label such as "wlan0 (192.168.1.20)".
func (i Iface) String() string { return i.Name + " (" + i.IP.String() + ")" }

// virtualPrefixes are interface names that belong to containers/VMs and are
// almost never reachable from a phone.
var virtualPrefixes = []string{"docker", "veth", "br-", "virbr", "vmnet", "vboxnet", "podman", "cni", "flannel", "tun", "tap"}

// LANInterfaces lists up, non-loopback IPv4 interfaces, private addresses
// first and virtual interfaces last.
func LANInterfaces() ([]Iface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, errors.NewErr("ListInterfacesFailed", err)
	}
	var candidates []Iface
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			return nil, errors.NewErr("ListInterfaceAddrsFailed", err).SetMetadata("interface", ifc.Name)
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				candidates = append(candidates, Iface{Name: ifc.Name, IP: n.IP})
			}
		}
	}
	res := Rank(candidates)
	if len(res) == 0 {
		return nil, errors.New("NoLanInterface", "no usable IPv4 network interface found")
	}
	return res, nil
}

// Rank keeps IPv4 non-loopback, non-link-local addresses and orders them by
// how likely a phone on the same Wi-Fi can reach them.
func Rank(in []Iface) []Iface {
	var out []Iface
	for _, c := range in {
		ip4 := c.IP.To4()
		if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() || ip4.IsUnspecified() {
			continue
		}
		out = append(out, Iface{Name: c.Name, IP: ip4})
	}
	score := func(i Iface) int {
		s := 0
		if isVirtual(i.Name) {
			s += 2
		}
		if !i.IP.IsPrivate() {
			s++
		}
		return s
	}
	slices.SortStableFunc(out, func(a, b Iface) int { return score(a) - score(b) })
	return out
}

func isVirtual(name string) bool {
	for _, p := range virtualPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
