package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"

	"github.com/hashicorp/yamux"
	"github.com/1mr0-tech/tether/internal/tunnel"
)

type Config struct {
	RelayAddr string
	PSK       string // pre-shared key — must match the relay server's PSK
}

type controlMsg struct {
	Action  string `json:"action"`  // "open" or "close"
	Session string `json:"session"`
	Port    int    `json:"port,omitempty"`
}

type controlResp struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// Run connects a persistent control channel to the relay and handles
// open/close commands to dynamically start/stop per-session listeners.
func Run(ctx context.Context, cfg Config) error {
	conn, err := net.Dial("tcp", cfg.RelayAddr)
	if err != nil {
		return fmt.Errorf("agent: dial relay: %w", err)
	}
	defer conn.Close()

	// Register as the persistent control connection — include PSK.
	if err := json.NewEncoder(conn).Encode(map[string]string{
		"role": "control",
		"psk":  cfg.PSK,
	}); err != nil {
		return fmt.Errorf("agent: control handshake: %w", err)
	}
	log.Printf("agent: control channel connected to %s", cfg.RelayAddr)

	var (
		mu       sync.Mutex
		sessions = make(map[string]context.CancelFunc)
	)

	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	for {
		var msg controlMsg
		if err := dec.Decode(&msg); err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return fmt.Errorf("agent: control read: %w", err)
			}
		}

		switch msg.Action {
		case "open":
			sCtx, cancel := context.WithCancel(ctx)
			mu.Lock()
			sessions[msg.Session] = cancel
			mu.Unlock()

			go func(sessionID string, port int, sCtx context.Context, cancelFn context.CancelFunc) {
				// Ensure cancel is always called when the session goroutine exits,
				// even if the "close" control message is never received (CWE-400).
				defer func() {
					cancelFn()
					mu.Lock()
					delete(sessions, sessionID)
					mu.Unlock()
				}()
				if err := runSession(sCtx, cfg.RelayAddr, cfg.PSK, sessionID, port); err != nil {
					log.Printf("agent: session %s: %v", sessionID, err)
				}
			}(msg.Session, msg.Port, sCtx, cancel)

			_ = enc.Encode(controlResp{Status: "ready"})

		case "close":
			mu.Lock()
			if cancel, ok := sessions[msg.Session]; ok {
				cancel()
				delete(sessions, msg.Session)
			}
			mu.Unlock()
			_ = enc.Encode(controlResp{Status: "closed"})

		default:
			log.Printf("agent: unknown control action %q", msg.Action)
			_ = enc.Encode(controlResp{Status: "error", Error: fmt.Sprintf("unknown action: %s", msg.Action)})
		}
	}
}

// runSession opens a yamux tunnel to the relay for a given session,
// then listens on the service port and forwards each connection as a new stream.
func runSession(ctx context.Context, relayAddr, psk, sessionID string, port int) error {
	relayConn, err := net.Dial("tcp", relayAddr)
	if err != nil {
		return fmt.Errorf("dial relay: %w", err)
	}

	if err := json.NewEncoder(relayConn).Encode(map[string]string{
		"role":    "agent",
		"session": sessionID,
		"psk":     psk,
	}); err != nil {
		_ = relayConn.Close()
		return fmt.Errorf("data handshake: %w", err)
	}

	// yamux client — opens one stream per inbound service connection.
	mux, err := yamux.Client(relayConn, nil)
	if err != nil {
		_ = relayConn.Close()
		return fmt.Errorf("yamux: %w", err)
	}
	defer mux.Close()

	// Listen on the service's target port.
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return fmt.Errorf("listen :%d: %w", port, err)
	}
	defer ln.Close()

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	log.Printf("agent: session %s listening on :%d", sessionID, port)

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return fmt.Errorf("accept: %w", err)
			}
		}
		go func(c net.Conn) {
			stream, err := mux.Open()
			if err != nil {
				log.Printf("agent: yamux open: %v", err)
				_ = c.Close()
				return
			}
			tunnel.Splice(c, stream)
		}(conn)
	}
}
