package main

import "testing"

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
