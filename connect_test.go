package main

import (
	"bytes"
	"strings"
	"testing"
)

// state builds the slice of Docker's container JSON the connect info reads.
func stateWithPorts(bindings map[string]string) containerState {
	var st containerState
	st.NetworkSettings.Ports = map[string][]struct {
		HostIP   string `json:"HostIp"`
		HostPort string `json:"HostPort"`
	}{}
	for containerPort, hostPort := range bindings {
		st.NetworkSettings.Ports[containerPort] = []struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		}{{HostIP: "0.0.0.0", HostPort: hostPort}}
	}
	return st
}

func TestConnectUsesThePublishedPortsNotTheImageDefaults(t *testing.T) {
	st := stateWithPorts(map[string]string{gamePortInContainer: "17777", queryPortInContainer: "27015"})
	info := gatherConnect(config{}, st, "192.168.1.20:8080")
	if !info.Known {
		t.Fatal("nothing was reported")
	}
	if info.GamePort != "17777" {
		t.Errorf("game port %q, want the published one", info.GamePort)
	}
	if info.Local != "127.0.0.1:17777" {
		t.Errorf("local %q", info.Local)
	}
	// The address the browser used is by definition one that works from where the operator sits.
	if info.LAN != "192.168.1.20:17777" {
		t.Errorf("lan %q", info.LAN)
	}
}

func TestConnectAlsoHandsOutTheAddressSteamAsksOver(t *testing.T) {
	// Steam's add-a-server dialog queries the query port and finds nothing on the game port, so the
	// page has to offer both addresses rather than leave the reader to swap the number themselves.
	st := stateWithPorts(map[string]string{gamePortInContainer: "7777", queryPortInContainer: "27015"})
	info := gatherConnect(config{publicHost: "ark.example.org"}, st, "192.168.1.20:8080")
	if info.LocalQuery != "127.0.0.1:27015" || info.LANQuery != "192.168.1.20:27015" || info.PublicQuery != "ark.example.org:27015" {
		t.Errorf("query addresses: local %q, lan %q, public %q", info.LocalQuery, info.LANQuery, info.PublicQuery)
	}

	// A deployment that publishes no query port has no such address, and the game port is not one.
	info = gatherConnect(config{}, stateWithPorts(map[string]string{gamePortInContainer: "7777"}), "192.168.1.20:8080")
	if info.LocalQuery != "" || info.LANQuery != "" {
		t.Errorf("invented a query address: local %q, lan %q", info.LocalQuery, info.LANQuery)
	}
}

// The page itself, because the addresses being right in the struct is not what anyone copies. The
// router test renders the status page without Docker access, where the whole connect block is
// skipped, so this is the only place the block is executed at all.
func TestStatusPageCarriesBothAddresses(t *testing.T) {
	st := serverStatus{Connect: gatherConnect(config{},
		stateWithPorts(map[string]string{gamePortInContainer: "7777", queryPortInContainer: "27015"}),
		"192.168.1.20:8080")}

	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, "status", st); err != nil {
		t.Fatalf("rendering the status page: %v", err)
	}
	for _, want := range []string{"192.168.1.20:7777", "192.168.1.20:27015", "127.0.0.1:27015"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("the page never names %s", want)
		}
	}
}

func TestConnectSaysNothingWithoutAPublishedGamePort(t *testing.T) {
	// A deployment that publishes no game port cannot be joined, and inventing 7777 would be a guess
	// about someone else's compose file.
	info := gatherConnect(config{}, stateWithPorts(map[string]string{queryPortInContainer: "27015"}), "host:8080")
	if info.Known || info.Local != "" {
		t.Errorf("reported an address anyway: %+v", info)
	}
}

func TestConnectDoesNotRepeatLoopbackAsTheNetworkAddress(t *testing.T) {
	st := stateWithPorts(map[string]string{gamePortInContainer: "7777"})
	for _, host := range []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080"} {
		if info := gatherConnect(config{}, st, host); info.LAN != "" {
			t.Errorf("%s produced a network address %q", host, info.LAN)
		}
	}
}

func TestConnectTakesTheOutsideAddressFromTheOperator(t *testing.T) {
	st := stateWithPorts(map[string]string{gamePortInContainer: "7777"})

	info := gatherConnect(config{}, st, "192.168.1.20:8080")
	if !info.PublicUnset || info.Public != "" {
		t.Errorf("an unset outside address was filled in: %+v", info)
	}

	info = gatherConnect(config{publicHost: "ark.example.org"}, st, "192.168.1.20:8080")
	if info.PublicUnset || info.Public != "ark.example.org:7777" {
		t.Errorf("public %q", info.Public)
	}
	// A value that already carries a port must not end up with two.
	info = gatherConnect(config{publicHost: "203.0.113.7:9999"}, st, "192.168.1.20:8080")
	if info.Public != "203.0.113.7:7777" {
		t.Errorf("public %q", info.Public)
	}
}

func TestConnectNamesTheLocalNetworkAddressWhenTheOperatorPinnedIt(t *testing.T) {
	st := stateWithPorts(map[string]string{gamePortInContainer: "7777"})

	// The usual case: the panel is published on the loopback, so it is opened as localhost, and
	// localhost says nothing about the network. Without a pinned address the page has to say so.
	info := gatherConnect(config{}, st, "127.0.0.1:8080")
	if !info.LANUnset || info.LAN != "" {
		t.Errorf("localhost produced a network address: %+v", info)
	}

	info = gatherConnect(config{lanHost: "192.168.178.20"}, st, "127.0.0.1:8080")
	if info.LANUnset || info.LAN != "192.168.178.20:7777" {
		t.Errorf("lan %q (unset=%v)", info.LAN, info.LANUnset)
	}

	// A pinned address wins over the one the browser used, because the operator set it on purpose.
	info = gatherConnect(config{lanHost: "192.168.178.20"}, st, "10.0.0.5:8080")
	if info.LAN != "192.168.178.20:7777" {
		t.Errorf("lan %q", info.LAN)
	}
}
