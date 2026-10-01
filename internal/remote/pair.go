package remote

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

// PairingLink is the atlascommander:// link the phone scans. hosts are
// "host:port" strings, most likely first; every value is URL-encoded, and the
// hosts are joined with a literal comma, which no host contains.
func PairingLink(name string, hosts []string, token, fingerprint string) string {
	esc := make([]string, len(hosts))
	for i, h := range hosts {
		esc[i] = url.QueryEscape(h)
	}
	return "atlascommander://pair?v=1&n=" + url.QueryEscape(name) +
		"&h=" + strings.Join(esc, ",") +
		"&t=" + url.QueryEscape(token) +
		"&f=" + url.QueryEscape(fingerprint)
}

// virtualPrefixes are interface names for container and VM bridges: a phone
// can't reach those, and a docker0 address would otherwise look like a LAN.
var virtualPrefixes = []string{"docker", "br-", "veth", "virbr", "podman", "cni", "flannel", "vmnet", "vboxnet"}

// Hosts lists this PC's addresses as host:port, LAN IPv4 first, then the
// others (Tailscale's 100.x, public or unique-local IPv6). Loopback and
// link-local addresses are left out, as are container bridges.
func Hosts(port int) []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var all []net.IP
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || isVirtual(ifc.Name) {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				all = append(all, ipn.IP)
			}
		}
	}
	return hostsFrom(all, port)
}

func isVirtual(name string) bool {
	for _, p := range virtualPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// hostsFrom orders and formats addresses; it is Hosts without the system
// calls, so it can be tested.
func hostsFrom(ips []net.IP, port int) []string {
	var lan, rest []string
	p := strconv.Itoa(port)
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
			continue
		}
		h := net.JoinHostPort(ip.String(), p)
		if ip4 := ip.To4(); ip4 != nil && ip4.IsPrivate() && !isCGNAT(ip4) {
			lan = append(lan, h)
		} else {
			rest = append(rest, h)
		}
	}
	// IPv4 before IPv6 within the rest, since the phone tries them in order.
	var v4, v6 []string
	for _, h := range rest {
		if host, _, _ := net.SplitHostPort(h); strings.Contains(host, ":") {
			v6 = append(v6, h)
		} else {
			v4 = append(v4, h)
		}
	}
	return append(append(lan, v4...), v6...)
}

// isCGNAT is 100.64.0.0/10, which Tailscale uses; it is not a LAN address.
func isCGNAT(ip net.IP) bool { return ip[0] == 100 && ip[1]&0xc0 == 64 }
