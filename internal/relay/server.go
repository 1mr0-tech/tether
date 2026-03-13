package relay

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"time"
)

const handshakeTimeout = 30 * time.Second

// Server is the relay TCP server. It bridges agent ↔ developer CLI connections
// and routes control messages from ops to the persistent agent control channel.
type Server struct {
	addr     string
	psk      string // pre-shared key; empty string disables PSK check (dev/test only)
	registry *Registry
}

func NewServer(addr, psk string) *Server {
	return &Server{addr: addr, psk: psk, registry: NewRegistry()}
}

func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("relay listen: %w", err)
	}
	log.Printf("relay server listening on %s", s.addr)
	for {
		conn, err := ln.Accept()
		if err != nil {
			return fmt.Errorf("relay accept: %w", err)
		}
		go s.handle(conn)
	}
}

// handshake is the first JSON message sent by every connecting party.
type handshake struct {
	Role    string `json:"role"`    // "control", "ops", "agent", "client"
	Action  string `json:"action"`  // ops only: "open" or "close"
	Session string `json:"session"` // agent, client, ops
	Port    int    `json:"port"`    // ops "open" only
	PSK     string `json:"psk"`     // pre-shared key — must match server's PSK
}

func (s *Server) handle(conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))

	var hs handshake
	if err := json.NewDecoder(conn).Decode(&hs); err != nil {
		log.Printf("relay: handshake error from %s: %v", conn.RemoteAddr(), err)
		_ = conn.Close()
		return
	}
	_ = conn.SetDeadline(time.Time{})

	// Reject connections with a wrong or missing PSK.
	if s.psk != "" && hs.PSK != s.psk {
		log.Printf("relay: rejected connection from %s: invalid PSK", conn.RemoteAddr())
		_ = conn.Close()
		return
	}

	switch hs.Role {

	case "control":
		log.Printf("relay: agent control connection established from %s", conn.RemoteAddr())
		s.registry.RunControl(conn)
		log.Printf("relay: agent control connection lost")
		_ = conn.Close()

	case "ops":
		s.handleOps(conn, hs.Action, hs.Session, hs.Port)
		_ = conn.Close()

	case "agent":
		if hs.Session == "" {
			_ = conn.Close()
			return
		}
		s.handleData(conn, hs.Session, "agent")

	case "client":
		if hs.Session == "" {
			_ = conn.Close()
			return
		}
		s.handleData(conn, hs.Session, "client")

	default:
		log.Printf("relay: unknown role %q from %s", hs.Role, conn.RemoteAddr())
		_ = conn.Close()
	}
}

func (s *Server) handleOps(conn net.Conn, action, session string, port int) {
	type opsResp struct {
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}
	respond := func(status, errMsg string) {
		_ = json.NewEncoder(conn).Encode(opsResp{Status: status, Error: errMsg})
	}

	if action != "open" && action != "close" {
		respond("error", fmt.Sprintf("unknown action %q", action))
		return
	}
	if session == "" {
		respond("error", "missing session")
		return
	}

	log.Printf("relay: ops %s session=%s port=%d", action, session, port)
	if err := s.registry.SendControl(action, session, port); err != nil {
		log.Printf("relay: control error: %v", err)
		respond("error", err.Error())
		return
	}
	respond("ok", "")
}

func (s *Server) handleData(conn net.Conn, session, role string) {
	var (
		entry *SessionEntry
		err   error
	)
	if role == "agent" {
		entry, err = s.registry.RegisterAgent(session, conn)
	} else {
		entry, err = s.registry.RegisterClient(session, conn)
	}
	if err != nil {
		log.Printf("relay: register %s: %v", role, err)
		_ = conn.Close()
		return
	}

	log.Printf("relay: session %s %s connected, waiting for peer", session, role)
	entry, err = s.registry.WaitForPeer(session, role)
	if err != nil {
		log.Printf("relay: %v", err)
		_ = conn.Close()
		return
	}

	if role == "client" {
		log.Printf("relay: session %s bridging", session)
		bridge(entry.AgentConn, entry.ClientConn)
		s.registry.delete(session)
	}
}

func bridge(a, b net.Conn) {
	defer a.Close()
	defer b.Close()
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
}
