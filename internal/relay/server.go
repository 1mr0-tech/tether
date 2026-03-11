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

type Server struct {
	addr     string
	registry *Registry
}

func NewServer(addr string) *Server {
	return &Server{addr: addr, registry: NewRegistry()}
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

// handshake is the first JSON message sent by any connecting party.
type handshake struct {
	Role    string `json:"role"`    // "control", "ops", "agent", "client"
	Action  string `json:"action"`  // ops only: "open" or "close"
	Session string `json:"session"` // agent, client, ops
	Port    int    `json:"port"`    // ops "open" only
}

func (s *Server) handle(conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))

	var hs handshake
	if err := json.NewDecoder(conn).Decode(&hs); err != nil {
		log.Printf("handshake error: %v", err)
		conn.Close()
		return
	}
	_ = conn.SetDeadline(time.Time{})

	switch hs.Role {

	case "control":
		// Persistent connection from the always-on agent pod.
		log.Printf("relay: agent control connection established")
		s.registry.RunControl(conn)
		log.Printf("relay: agent control connection lost")
		conn.Close()

	case "ops":
		// One-off command from ops CLI (start/stop).
		s.handleOps(conn, hs.Action, hs.Session, hs.Port)
		conn.Close()

	case "agent":
		// Per-session data connection from agent (one per active intercept connection).
		if hs.Session == "" {
			conn.Close()
			return
		}
		s.handleData(conn, hs.Session, "agent")

	case "client":
		// Developer CLI data connection.
		if hs.Session == "" {
			conn.Close()
			return
		}
		s.handleData(conn, hs.Session, "client")

	default:
		log.Printf("relay: unknown role %q", hs.Role)
		conn.Close()
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

	log.Printf("relay: ops command action=%s session=%s port=%d", action, session, port)
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
		conn.Close()
		return
	}

	log.Printf("relay: session %s %s connected, waiting for peer", session, role)
	entry, err = s.registry.WaitForPeer(session, role)
	if err != nil {
		log.Printf("relay: %v", err)
		conn.Close()
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
