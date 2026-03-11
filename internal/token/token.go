package token

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// Session is the opaque token handed to developers.
// It encodes everything needed to connect — relay address and session ID.
type Session struct {
	ID    string `json:"id"`
	Relay string `json:"relay"`
}

func Encode(t Session) string {
	data, _ := json.Marshal(t)
	return base64.RawURLEncoding.EncodeToString(data)
}

func Decode(s string) (Session, error) {
	data, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Session{}, fmt.Errorf("invalid session token")
	}
	var t Session
	if err := json.Unmarshal(data, &t); err != nil {
		return Session{}, fmt.Errorf("invalid session token")
	}
	if t.ID == "" || t.Relay == "" {
		return Session{}, fmt.Errorf("invalid session token: missing fields")
	}
	return t, nil
}
