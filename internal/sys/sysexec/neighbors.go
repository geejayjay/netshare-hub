package sysexec

import (
	"bufio"
	"net"
	"net/netip"
	"os"
	"strings"
)

// Neighbors reads the kernel's IPv4 neighbour (ARP) table. Any process may
// read it, without privileges or network sockets, so the unprivileged web
// server uses it directly.
type Neighbors struct {
	path string
}

func NewNeighbors() Neighbors { return Neighbors{path: "/proc/net/arp"} }

// MAC returns the hardware address the server last saw for ip.
func (n Neighbors) MAC(ip netip.Addr) (string, bool) {
	ip = ip.Unmap()
	if !ip.Is4() {
		return "", false
	}
	f, err := os.Open(n.path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	return parseARP(bufio.NewScanner(f), ip)
}

// parseARP reads /proc/net/arp:
//
//	IP address       HW type     Flags       HW address            Mask     Device
//	192.168.1.20     0x1         0x2         aa:bb:cc:dd:ee:ff     *        eth0
func parseARP(sc *bufio.Scanner, ip netip.Addr) (string, bool) {
	want := ip.String()
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || f[0] != want {
			continue
		}
		// Flags 0x2 = complete; an incomplete entry has no address yet.
		if f[2] == "0x0" {
			return "", false
		}
		hw, err := net.ParseMAC(f[3])
		if err != nil || len(hw) != 6 || f[3] == "00:00:00:00:00:00" {
			return "", false
		}
		return hw.String(), true
	}
	return "", false
}
