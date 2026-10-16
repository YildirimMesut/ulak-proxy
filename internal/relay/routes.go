package relay

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

type routeStore struct {
	mu     sync.Mutex
	path   string
	routes map[string][]string // tunName → []CIDR
}

func newRouteStore(path string) *routeStore {
	rs := &routeStore{path: path, routes: make(map[string][]string)}
	rs.load()
	return rs
}

func (rs *routeStore) load() {
	data, err := os.ReadFile(rs.path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[!] routes: load %s: %v", rs.path, err)
		}
		return
	}
	if err := json.Unmarshal(data, &rs.routes); err != nil {
		log.Printf("[!] routes: parse: %v", err)
		rs.routes = make(map[string][]string)
	}
	total := 0
	for _, v := range rs.routes {
		total += len(v)
	}
	log.Printf("[*] routes: %d kayıtlı route yüklendi (%s)", total, rs.path)
}

func (rs *routeStore) save() {
	data, _ := json.MarshalIndent(rs.routes, "", "  ")
	os.WriteFile(rs.path, data, 0644)
}

func (rs *routeStore) Add(tunName, subnet string) error {
	if err := applyRoute(tunName, subnet); err != nil {
		return err
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, s := range rs.routes[tunName] {
		if s == subnet {
			return nil
		}
	}
	rs.routes[tunName] = append(rs.routes[tunName], subnet)
	rs.save()
	return nil
}

func (rs *routeStore) Delete(tunName, subnet string) error {
	deleteRoute(tunName, subnet)
	rs.mu.Lock()
	defer rs.mu.Unlock()
	list := rs.routes[tunName]
	kept := list[:0]
	for _, s := range list {
		if s != subnet {
			kept = append(kept, s)
		}
	}
	rs.routes[tunName] = kept
	rs.save()
	return nil
}

func (rs *routeStore) ApplyAll(tunName string) {
	rs.mu.Lock()
	list := make([]string, len(rs.routes[tunName]))
	copy(list, rs.routes[tunName])
	rs.mu.Unlock()
	for _, s := range list {
		if err := applyRoute(tunName, s); err != nil {
			log.Printf("[!] routes: re-apply %s via %s: %v", s, tunName, err)
		} else {
			log.Printf("[*] routes: re-applied %s via %s", s, tunName)
		}
	}
}

func (rs *routeStore) List(tunName string) []string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	cp := make([]string, len(rs.routes[tunName]))
	copy(cp, rs.routes[tunName])
	return cp
}

func (rs *routeStore) ListAll() map[string][]string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make(map[string][]string)
	for k, v := range rs.routes {
		cp := make([]string, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}

func applyRoute(tunName, subnet string) error {
	if runtime.GOOS == "windows" {
		return applyRouteWindows(tunName, subnet)
	}
	return applyRouteLinux(tunName, subnet)
}

func applyRouteLinux(tunName, subnet string) error {
	// "replace" overwrites any existing route for this subnet (e.g. from ligolo or a
	// previous relay run) rather than failing with "File exists".
	out, err := exec.Command("ip", "route", "replace", subnet, "dev", tunName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ip route replace %s dev %s: %s", subnet, tunName, strings.TrimSpace(string(out)))
	}
	return nil
}

func applyRouteWindows(tunName, subnet string) error {
	// On-link routes (nexthop=0.0.0.0) fail because Windows tries to ARP for the
	// destination on the TAP interface, but the TAP driver only handles ARP for its
	// own /30 subnet. Using the peer IP (agent virtual IP) as nexthop routes
	// through an address the TAP driver already knows how to resolve.
	gateway := windowsInterfacePeerIP(tunName)

	// Remove any stale route for this subnet before adding (old nexthop may differ).
	exec.Command("netsh", "interface", "ipv4", "delete", "route", subnet, tunName).Run()

	var args []string
	if gateway != "" {
		args = []string{"interface", "ipv4", "add", "route", subnet, tunName, gateway}
	} else {
		args = []string{"interface", "ipv4", "add", "route", subnet, tunName}
	}
	out, err := exec.Command("netsh", args...).CombinedOutput()
	if err != nil {
		msg := string(out)
		lower := strings.ToLower(msg)
		if strings.Contains(lower, "already") || strings.Contains(lower, "zaten") {
			return nil
		}
		return fmt.Errorf("netsh route add %s %s: %s", subnet, tunName, msg)
	}
	return nil
}

// windowsInterfacePeerIP returns the peer IP for a /30 point-to-point interface.
// For 10.224.0.1/30 it returns 10.224.0.2, and vice-versa (XOR the last octet with 3).
func windowsInterfacePeerIP(tunName string) string {
	iface, err := net.InterfaceByName(tunName)
	if err != nil {
		return ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		ipnet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip4 := ipnet.IP.To4()
		if ip4 == nil {
			continue
		}
		ones, _ := ipnet.Mask.Size()
		if ones == 30 {
			peer := make(net.IP, 4)
			copy(peer, ip4)
			peer[3] ^= 3 // 1↔2 within /30 block
			return peer.String()
		}
	}
	return ""
}

func deleteRoute(tunName, subnet string) {
	if runtime.GOOS == "windows" {
		gateway := windowsInterfacePeerIP(tunName)
		if gateway != "" {
			exec.Command("netsh", "interface", "ipv4", "delete", "route",
				subnet, tunName, gateway).Run()
		}
		exec.Command("netsh", "interface", "ipv4", "delete", "route",
			subnet, tunName).Run()
	} else {
		exec.Command("ip", "route", "del", subnet, "dev", tunName).Run()
	}
}
