package sys

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// Network rules: which computers may connect to a share.
//
// A share has two lists of addresses (single IPs or CIDR ranges, IPv4 or
// IPv6):
//
//	allow  if any: ONLY these may connect
//	block  never these (inside the allowed ranges, or anywhere when
//	       there is no allow list)
//
// Samba's own combination of "hosts allow" and "hosts deny" is surprising
// (with both set, an address on neither list is let in), so the lists are
// rendered as "hosts allow = <allow> EXCEPT <block>", or "hosts deny =
// <block>" when nothing is allowed explicitly. HostAllowed is the same rule
// in Go, for the web file manager.
//
// Samba always lets in the server itself (127.0.0.1 and ::1) unless it is
// blocked by "hosts deny"; that only matters for clients on the server.

// MaxHostRules is the most entries one list may have.
const MaxHostRules = 64

var errHostRule = errors.New("Enter an IP address like 192.168.1.50 or a range like 192.168.1.0/24.")

// ParseHostRule checks one network rule entry and returns its canonical
// form: "192.168.1.50", "192.168.1.0/24", "2001:db8::/32". Host names,
// wildcards, Samba keywords (ALL, EXCEPT, LOCAL) and anything else are
// refused. The error is a message for the user.
func ParseHostRule(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 64 {
		return "", errHostRule
	}
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil || p.Addr().Is4In6() {
			return "", errHostRule
		}
		if m := p.Masked(); m != p {
			return "", fmt.Errorf("This range has bits set after the prefix. Did you mean %s?", m)
		}
		if p.IsSingleIP() {
			return p.Addr().String(), nil
		}
		return p.String(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" || a.Is4In6() {
		return "", errHostRule
	}
	return a.String(), nil
}

func checkHostList(share, name string, list []string) error {
	if len(list) > MaxHostRules {
		return fmt.Errorf("share %q: more than %d %s entries", share, MaxHostRules, name)
	}
	seen := map[string]bool{}
	for _, entry := range list {
		canonical, err := ParseHostRule(entry)
		if err != nil || canonical != entry {
			return fmt.Errorf("share %q: unsafe %s entry %q", share, name, entry)
		}
		if seen[entry] {
			return fmt.Errorf("share %q: %s entry %q is listed twice", share, name, entry)
		}
		seen[entry] = true
	}
	return nil
}

func prefixOf(entry string) (netip.Prefix, bool) {
	if p, err := netip.ParsePrefix(entry); err == nil {
		return p, true
	}
	a, err := netip.ParseAddr(entry)
	if err != nil {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(a, a.BitLen()), true
}

func matches(list []string, ip netip.Addr) bool {
	for _, entry := range list {
		if p, ok := prefixOf(entry); ok && p.Contains(ip) {
			return true
		}
	}
	return false
}

// HostAllowed reports whether a client address may use a share with these
// rules. Unlike Samba it makes no exception for the server's own address:
// for the web file manager the client is whoever made the HTTP request.
func HostAllowed(allow, block []string, ip netip.Addr) bool {
	ip = ip.Unmap()
	if len(allow) > 0 && !matches(allow, ip) {
		return false
	}
	return !matches(block, ip)
}
