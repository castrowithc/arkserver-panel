// RCON client for the ARK server: Valve Source RCON over TCP, standard library only. Carries the
// player list and the restart action (SaveWorld then DoExit, with arkmanager's autorestart bringing
// the process back). Reaching the server this way needs no Docker access at all.
package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"
	"time"
)

// Source RCON packet types. SERVERDATA_AUTH_RESPONSE and SERVERDATA_EXECCOMMAND share the value 2;
// they never collide because one is only ever read and the other only ever written.
const (
	rconResponseValue = 0
	rconExecCommand   = 2
	rconAuthResponse  = 2
	rconAuth          = 3
)

// The protocol caps a packet at 4096 bytes and splits longer replies across several. Reassembly
// needs a sentinel-packet trick; at the player counts this panel targets a reply never gets that
// big, so one packet per command is read and the trick is left unbuilt.
const rconMaxPacket = 4096

type rconConfig struct {
	addr        string
	pass        string
	dialTimeout time.Duration
	// Budgets bound a whole exchange, connect excluded, rather than a single read: auth and the
	// command together, so the ceiling is the one the caller actually expects.
	//
	// They differ because the two callers want opposite things. While the server reloads its world
	// it passes through a phase where it accepts the connection but answers nothing (seen during
	// the live restart test), so a status poll has to give up quickly and report "restarting"
	// instead of blocking the page. A restart, meanwhile, must tolerate a SaveWorld that
	// legitimately runs long on a large world.
	statusBudget  time.Duration
	commandBudget time.Duration
}

func (c rconConfig) configured() bool { return c.addr != "" && c.pass != "" }

type rconConn struct {
	conn net.Conn
	id   int32
}

// dialRCON connects and authenticates, giving the caller budget for everything that follows on this
// connection. The caller closes the result. Each operation opens its own connection: the monitor
// polls at a human interval, so a short-lived connection costs nothing and saves having to detect
// and recover a stale one.
func dialRCON(cfg rconConfig, budget time.Duration) (*rconConn, error) {
	conn, err := net.DialTimeout("tcp", cfg.addr, cfg.dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("rcon: dial %s: %w", cfg.addr, err)
	}
	// One absolute deadline for the whole exchange, never pushed back, so a server that accepts
	// the connection and then goes quiet cannot hold the caller past the budget.
	if err := conn.SetDeadline(time.Now().Add(budget)); err != nil {
		conn.Close()
		return nil, err
	}
	c := &rconConn{conn: conn}
	if err := c.auth(cfg.pass); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

func (c *rconConn) Close() error { return c.conn.Close() }

func (c *rconConn) auth(pass string) error {
	want, err := c.send(rconAuth, pass)
	if err != nil {
		return err
	}
	// Classic Source servers send an empty RESPONSE_VALUE ahead of the verdict, ARK sends only the
	// verdict. Skip whatever is not the verdict instead of depending on either behaviour.
	for {
		id, typ, _, err := c.recv()
		if err != nil {
			return err
		}
		if typ != rconAuthResponse {
			continue
		}
		if id != want { // the server answers -1 on a bad password
			return errors.New("rcon: authentication failed")
		}
		return nil
	}
}

// exec runs one command and returns the server's reply.
func (c *rconConn) exec(cmd string) (string, error) {
	want, err := c.send(rconExecCommand, cmd)
	if err != nil {
		return "", err
	}
	for {
		id, typ, body, err := c.recv()
		if err != nil {
			return "", err
		}
		if typ == rconResponseValue && id == want {
			return strings.TrimSpace(body), nil
		}
	}
}

// send writes one packet: size, id, type, body, then the two terminating nulls. The size counts
// everything after itself.
func (c *rconConn) send(typ int32, body string) (int32, error) {
	c.id++
	pkt := make([]byte, 0, 14+len(body))
	pkt = binary.LittleEndian.AppendUint32(pkt, uint32(10+len(body)))
	pkt = binary.LittleEndian.AppendUint32(pkt, uint32(c.id))
	pkt = binary.LittleEndian.AppendUint32(pkt, uint32(typ))
	pkt = append(pkt, body...)
	pkt = append(pkt, 0, 0)
	if _, err := c.conn.Write(pkt); err != nil {
		return 0, fmt.Errorf("rcon: write: %w", err)
	}
	return c.id, nil
}

func (c *rconConn) recv() (int32, int32, string, error) {
	var head [4]byte
	if _, err := io.ReadFull(c.conn, head[:]); err != nil {
		return 0, 0, "", fmt.Errorf("rcon: read size: %w", err)
	}
	size := binary.LittleEndian.Uint32(head[:])
	if size < 10 || size > rconMaxPacket {
		return 0, 0, "", fmt.Errorf("rcon: implausible packet size %d", size)
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(c.conn, buf); err != nil {
		return 0, 0, "", fmt.Errorf("rcon: read body: %w", err)
	}
	id := int32(binary.LittleEndian.Uint32(buf[0:4]))
	typ := int32(binary.LittleEndian.Uint32(buf[4:8]))
	return id, typ, string(bytes.TrimRight(buf[8:], "\x00")), nil
}

// listPlayers reports who is online.
func listPlayers(cfg rconConfig) ([]string, error) {
	c, err := dialRCON(cfg, cfg.statusBudget)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	out, err := c.exec("ListPlayers")
	if err != nil {
		return nil, err
	}
	return parsePlayers(out), nil
}

// parsePlayers turns ARK's reply into names. Each line reads "0. Name, 7656119...", and an empty
// server answers with a "No Players Connected" notice instead of a list.
func parsePlayers(out string) []string {
	if out == "" || strings.Contains(out, "No Players Connected") {
		return nil
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, rest, ok := strings.Cut(line, ". "); ok {
			line = rest
		}
		if name, _, ok := strings.Cut(line, ","); ok {
			line = name
		}
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	return names
}

// restartServer saves the world and quits the game process. The container stays up and arkmanager's
// autorestart pulls the process straight back; expect roughly four to five minutes until players
// can join again, dominated by the world load.
func restartServer(cfg rconConfig) error {
	c, err := dialRCON(cfg, cfg.commandBudget)
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err := c.exec("SaveWorld"); err != nil {
		return fmt.Errorf("rcon: saveworld: %w", err)
	}
	// DoExit kills the very process that would answer, so a dropped connection here is the
	// expected outcome and not a failure.
	if _, err := c.exec("DoExit"); err != nil && !connectionDropped(err) {
		return fmt.Errorf("rcon: doexit: %w", err)
	}
	return nil
}

func connectionDropped(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET)
}
