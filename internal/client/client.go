package client

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"

	"github.com/hashicorp/yamux"
	"github.com/1mr0-tech/tether/internal/tunnel"
)

type Config struct {
	RelayAddr  string
	SessionID  string
	LocalPort  int
}

// Run connects to the relay, acts as yamux server (accepts streams),
// and forwards each stream to localhost:<LocalPort>.
func Run(ctx context.Context, cfg Config) error {
	relayConn, err := net.Dial("tcp", cfg.RelayAddr)
	if err != nil {
		return fmt.Errorf("client: dial relay: %w", err)
	}
	defer relayConn.Close()

	// Send handshake
	hs := map[string]interface{}{
		"role":    "client",
		"session": cfg.SessionID,
		"port":    cfg.LocalPort,
	}
	if err := json.NewEncoder(relayConn).Encode(hs); err != nil {
		return fmt.Errorf("client: handshake: %w", err)
	}

	// CLI is yamux server — accepts streams opened by agent
	mux, err := yamux.Server(relayConn, nil)
	if err != nil {
		return fmt.Errorf("client: yamux server: %w", err)
	}
	defer mux.Close()

	log.Printf("client: connected to relay, forwarding to localhost:%d", cfg.LocalPort)

	go func() {
		<-ctx.Done()
		mux.Close()
	}()

	for {
		stream, err := mux.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return fmt.Errorf("client: accept yamux stream: %w", err)
			}
		}
		go func(s net.Conn) {
			local, err := net.Dial("tcp", fmt.Sprintf("localhost:%d", cfg.LocalPort))
			if err != nil {
				log.Printf("client: dial local port %d: %v", cfg.LocalPort, err)
				s.Close()
				return
			}
			tunnel.Splice(s, local)
		}(stream)
	}
}
