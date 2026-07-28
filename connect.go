// Where to point the game client. Four situations have four different answers, and the panel can be
// exact about the first two, honest about the third and silent about none of them:
//
//   - On the machine that hosts the server, where host and client are the same computer.
//   - From another device on the same network.
//   - From outside, over the internet.
//   - On a rented server, where outside is the only way in.
//
// The ports come from what the host actually published, not from the image's defaults, because the
// deployment may publish others. The address for the local network is the one the browser used to
// reach this page, which is by definition an address that works from where the operator is sitting.
// The address from outside is the one thing the panel cannot know: looking it up would mean a service
// that is deliberately bound to the local network asking an outside party for its own address, so it
// is named by the operator or left open.
package main

import (
	"net"
	"strings"
)

const (
	gamePortInContainer  = "7777/udp"
	queryPortInContainer = "27015/udp"
)

type connectInfo struct {
	GamePort  string
	QueryPort string
	// Local, LAN and Public are ready to read out loud, host and port together. An empty one is not
	// rendered rather than rendered as a half address.
	Local  string
	LAN    string
	Public string
	// The same three hosts with the query port. Steam's own "add a server" dialog asks over that port
	// and finds nothing on the game port, so handing out only the game address is what sends someone
	// looking for a server that is running. Empty when the deployment publishes no query port.
	LocalQuery  string
	LANQuery    string
	PublicQuery string
	// PublicUnset says the operator has not named the outside address, so the page can say what to
	// set instead of leaving a gap.
	PublicUnset bool
	// LANUnset means the address inside the local network could not be worked out, which is the
	// normal case when the panel is opened as localhost. The page then says how to find it, because
	// this is exactly the address a second device in the same household needs.
	LANUnset bool
	Known    bool
}

// gatherConnect builds the addresses. requestHost is the Host the browser used, which carries the
// panel's port and therefore has to be stripped down to the host part.
func gatherConnect(cfg config, st containerState, requestHost string) connectInfo {
	game, query := st.publishedPort(gamePortInContainer), st.publishedPort(queryPortInContainer)
	if game == "" {
		// Without a published game port there is nothing to hand out, and inventing 7777 would be a
		// guess about someone else's deployment.
		return connectInfo{}
	}

	// Both addresses for one host: the game port to join on, the query port to be found on.
	addrs := func(host string) (string, string) {
		if query == "" {
			return net.JoinHostPort(host, game), ""
		}
		return net.JoinHostPort(host, game), net.JoinHostPort(host, query)
	}

	info := connectInfo{GamePort: game, QueryPort: query, Known: true}
	info.Local, info.LocalQuery = addrs("127.0.0.1")
	switch host := hostOnly(requestHost); {
	case cfg.lanHost != "":
		info.LAN, info.LANQuery = addrs(hostOnly(cfg.lanHost))
	case host != "" && !isLoopback(host):
		// The address the browser used is by definition one that works from where the operator sits.
		info.LAN, info.LANQuery = addrs(host)
	default:
		info.LANUnset = true
	}
	switch {
	case cfg.publicHost != "":
		info.Public, info.PublicQuery = addrs(hostOnly(cfg.publicHost))
	default:
		info.PublicUnset = true
	}
	return info
}

// hostOnly drops a port from a host:port pair and the brackets from an IPv6 literal, and leaves a
// bare host alone.
func hostOnly(hostPort string) string {
	if host, _, err := net.SplitHostPort(hostPort); err == nil {
		return host
	}
	return strings.Trim(hostPort, "[]")
}

// isLoopback keeps the local address from being offered twice: reaching the panel on localhost says
// nothing about how the machine is reachable from the network.
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
