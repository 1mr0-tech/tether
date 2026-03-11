package relay

import (
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

const peerTimeout = 2 * time.Minute

// controlMsg is sent from relay → agent over the persistent control connection.
type controlMsg struct {
	Action  string `json:"action"`  // "open" or "close"
	Session string `json:"session"`
	Port    int    `json:"port,omitempty"`
}

// controlResp is sent from agent → relay in response to a control message.
type controlResp struct {
	Status string `json:"status"` // "ready", "closed", or "error"
	Error  string `json:"error,omitempty"`
}

type controlRequest struct {
	msg    controlMsg
	respCh chan controlResult
}

type controlResult struct {
	resp controlResp
	err  error
}

// SessionEntry holds the paired agent+client data connections for a session.
type SessionEntry struct {
	AgentConn   net.Conn
	ClientConn  net.Conn
	agentReady  chan struct{}
	clientReady chan struct{}
}

// Registry manages the persistent agent control channel and all data sessions.
type Registry struct {
	mu       sync.Mutex
	sessions map[string]*SessionEntry

	// Control channel — one persistent connection from the always-on agent pod.
	controlCh    chan controlRequest
	controlReady chan struct{}
	controlOnce  sync.Once
}

func NewRegistry() *Registry {
	return &Registry{
		sessions:     make(map[string]*SessionEntry),
		controlCh:    make(chan controlRequest),
		controlReady: make(chan struct{}),
	}
}

// RunControl is called by the relay when the agent's persistent control connection arrives.
// It owns all I/O on that connection until it drops.
func (r *Registry) RunControl(conn net.Conn) {
	r.controlOnce.Do(func() { close(r.controlReady) })

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)

	for req := range r.controlCh {
		if err := enc.Encode(req.msg); err != nil {
			req.respCh <- controlResult{err: fmt.Errorf("write to agent: %w", err)}
			return
		}
		var resp controlResp
		if err := dec.Decode(&resp); err != nil {
			req.respCh <- controlResult{err: fmt.Errorf("read agent response: %w", err)}
			return
		}
		if resp.Status == "error" {
			req.respCh <- controlResult{err: fmt.Errorf("agent error: %s", resp.Error)}
		} else {
			req.respCh <- controlResult{resp: resp}
		}
	}
}

// SendControl sends an open/close command to the agent and waits for acknowledgement.
func (r *Registry) SendControl(action, session string, port int) error {
	select {
	case <-r.controlReady:
	case <-time.After(10 * time.Second):
		return fmt.Errorf("no agent connected — run 'tether install' first")
	}

	respCh := make(chan controlResult, 1)
	req := controlRequest{
		msg:    controlMsg{Action: action, Session: session, Port: port},
		respCh: respCh,
	}

	select {
	case r.controlCh <- req:
	case <-time.After(10 * time.Second):
		return fmt.Errorf("control channel busy")
	}

	select {
	case result := <-respCh:
		return result.err
	case <-time.After(30 * time.Second):
		return fmt.Errorf("timed out waiting for agent response")
	}
}

// --- Data session pairing (agent ↔ client per session) ---

func (r *Registry) getOrCreate(id string) *SessionEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.sessions[id]; ok {
		return e
	}
	e := &SessionEntry{
		agentReady:  make(chan struct{}),
		clientReady: make(chan struct{}),
	}
	r.sessions[id] = e
	return e
}

func (r *Registry) delete(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, id)
}

func (r *Registry) RegisterAgent(id string, conn net.Conn) (*SessionEntry, error) {
	e := r.getOrCreate(id)
	r.mu.Lock()
	if e.AgentConn != nil {
		r.mu.Unlock()
		return nil, fmt.Errorf("session %s: agent already registered", id)
	}
	e.AgentConn = conn
	close(e.agentReady)
	r.mu.Unlock()
	return e, nil
}

func (r *Registry) RegisterClient(id string, conn net.Conn) (*SessionEntry, error) {
	e := r.getOrCreate(id)
	r.mu.Lock()
	if e.ClientConn != nil {
		r.mu.Unlock()
		return nil, fmt.Errorf("session %s: client already registered", id)
	}
	e.ClientConn = conn
	close(e.clientReady)
	r.mu.Unlock()
	return e, nil
}

func (r *Registry) WaitForPeer(id string, role string) (*SessionEntry, error) {
	e := r.getOrCreate(id)
	var ready chan struct{}
	if role == "agent" {
		ready = e.clientReady
	} else {
		ready = e.agentReady
	}
	select {
	case <-ready:
		return e, nil
	case <-time.After(peerTimeout):
		r.delete(id)
		return nil, fmt.Errorf("session %s: timed out waiting for peer", id)
	}
}
