package main

import (
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeRCON serves one connection speaking the protocol, so the client is exercised over a real
// socket rather than against a hand-rolled mock of itself. authOK false makes the server reject the
// password the way a real one does, with id -1. replies answers commands by exact match.
func fakeRCON(t *testing.T, authOK bool, replies map[string]string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			id, typ, body, err := readPacket(conn)
			if err != nil {
				return
			}
			switch typ {
			case rconAuth:
				if !authOK {
					id = -1
				}
				writePacket(conn, id, rconAuthResponse, "")
			case rconExecCommand:
				writePacket(conn, id, rconResponseValue, replies[body])
			}
		}
	}()
	return ln.Addr().String()
}

func readPacket(r io.Reader) (int32, int32, string, error) {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, 0, "", err
	}
	buf := make([]byte, binary.LittleEndian.Uint32(head[:]))
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, 0, "", err
	}
	id := int32(binary.LittleEndian.Uint32(buf[0:4]))
	typ := int32(binary.LittleEndian.Uint32(buf[4:8]))
	return id, typ, strings.TrimRight(string(buf[8:]), "\x00"), nil
}

func writePacket(w io.Writer, id, typ int32, body string) {
	pkt := make([]byte, 0, 14+len(body))
	pkt = binary.LittleEndian.AppendUint32(pkt, uint32(10+len(body)))
	pkt = binary.LittleEndian.AppendUint32(pkt, uint32(id))
	pkt = binary.LittleEndian.AppendUint32(pkt, uint32(typ))
	pkt = append(pkt, body...)
	pkt = append(pkt, 0, 0)
	w.Write(pkt)
}

// silentListener accepts a connection and then never says anything, the way ARK behaves while it
// reloads its world.
func silentListener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		<-make(chan struct{}) // hold the connection open until the test ends
		conn.Close()
	}()
	return ln.Addr().String()
}

func testConfig(addr string) rconConfig {
	return rconConfig{
		addr: addr, pass: "secret",
		dialTimeout: 2 * time.Second, statusBudget: 2 * time.Second, commandBudget: 2 * time.Second,
	}
}

func TestListPlayers(t *testing.T) {
	addr := fakeRCON(t, true, map[string]string{
		"ListPlayers": "0. Alice, 76561198000000001\n1. Bob, 76561198000000002\n",
	})
	got, err := listPlayers(testConfig(addr))
	if err != nil {
		t.Fatalf("listPlayers: %v", err)
	}
	if len(got) != 2 || got[0] != "Alice" || got[1] != "Bob" {
		t.Errorf("want [Alice Bob], got %q", got)
	}
}

func TestListPlayersEmptyServer(t *testing.T) {
	addr := fakeRCON(t, true, map[string]string{"ListPlayers": "No Players Connected"})
	got, err := listPlayers(testConfig(addr))
	if err != nil {
		t.Fatalf("listPlayers: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want nobody online, got %q", got)
	}
}

func TestAuthFailure(t *testing.T) {
	addr := fakeRCON(t, false, nil)
	if _, err := listPlayers(testConfig(addr)); err == nil {
		t.Fatal("want an error on a rejected password, got none")
	}
}

func TestDialFailure(t *testing.T) {
	// Port 0 never accepts, so this exercises the unreachable-server path the UI must degrade on.
	cfg := testConfig("127.0.0.1:0")
	if _, err := listPlayers(cfg); err == nil {
		t.Fatal("want an error against an unreachable server, got none")
	}
}

// A reloading ARK server accepts the connection well before it answers on it, seen during the live
// restart test. A status poll must give up inside its budget rather than hold the page, and the
// budget has to cover the whole exchange: without an absolute deadline the silent auth and the
// silent command would each get the full wait and double it.
func TestStatusGivesUpOnASilentServer(t *testing.T) {
	cfg := testConfig(silentListener(t))
	cfg.statusBudget = 300 * time.Millisecond
	start := time.Now()
	if _, err := listPlayers(cfg); err == nil {
		t.Fatal("want an error against a silent server, got none")
	}
	if waited := time.Since(start); waited > time.Second {
		t.Errorf("want a give-up inside the budget, waited %s", waited)
	}
}

func TestRestartServer(t *testing.T) {
	addr := fakeRCON(t, true, map[string]string{
		"SaveWorld": "World Saved",
		"DoExit":    "Exiting",
	})
	if err := restartServer(testConfig(addr)); err != nil {
		t.Fatalf("restartServer: %v", err)
	}
}

// A real server dies mid-DoExit and the connection drops without a reply. That is the expected
// outcome, so it must not surface as a failed restart.
func TestRestartServerToleratesDropAfterDoExit(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			id, typ, body, err := readPacket(conn)
			if err != nil {
				return
			}
			if typ == rconAuth {
				writePacket(conn, id, rconAuthResponse, "")
				continue
			}
			if body == "DoExit" {
				return // hang up instead of replying, like a process that just exited
			}
			writePacket(conn, id, rconResponseValue, "World Saved")
		}
	}()
	if err := restartServer(testConfig(ln.Addr().String())); err != nil {
		t.Fatalf("a drop after DoExit must count as success, got %v", err)
	}
}

func TestParsePlayers(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want []string
	}{
		{"empty reply", "", nil},
		{"notice", "No Players Connected", nil},
		{"single", "0. Solo, 76561198000000001", []string{"Solo"}},
		{"name with a dot", "0. Dr. Who, 76561198000000001", []string{"Dr. Who"}},
		{"blank lines skipped", "\n0. Alice, 1\n\n", []string{"Alice"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePlayers(tt.out)
			if len(got) != len(tt.want) {
				t.Fatalf("want %q, got %q", tt.want, got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("want %q, got %q", tt.want, got)
				}
			}
		})
	}
}
