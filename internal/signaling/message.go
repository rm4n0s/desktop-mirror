// Package signaling implements the HTTPS server that serves the phone page
// and relays SDP/ICE messages over a WebSocket.
package signaling

import (
	"github.com/rm4n0s/errors"
)

// Message types exchanged over the WebSocket.
const (
	TypeHello     = "hello"
	TypeOffer     = "offer"
	TypeAnswer    = "answer"
	TypeCandidate = "candidate"
	TypeBye       = "bye"
	TypeError     = "error"
)

// Error codes carried by TypeError messages.
const (
	CodeSessionBusy = "session_busy"
	CodeTokenExpire = "token_expired"
)

// Message is the JSON envelope for every signaling frame.
type Message struct {
	Type string `json:"type"`

	// hello
	UserAgent string `json:"ua,omitempty"`

	// offer / answer
	SDP string `json:"sdp,omitempty"`

	// candidate (an empty Candidate marks the end of candidates)
	Candidate        string  `json:"candidate,omitempty"`
	SDPMid           *string `json:"sdpMid,omitempty"`
	SDPMLineIndex    *uint16 `json:"sdpMLineIndex,omitempty"`
	UsernameFragment *string `json:"usernameFragment,omitempty"`

	// error
	Code string `json:"code,omitempty"`
	Text string `json:"message,omitempty"`
}

// Validate checks the message has a known type and the fields it requires.
func (m Message) Validate() error {
	switch m.Type {
	case TypeHello, TypeBye, TypeCandidate:
		return nil
	case TypeOffer, TypeAnswer:
		if m.SDP == "" {
			return errors.New("SignalingMessageInvalid", "SDP message without sdp", "type", m.Type)
		}
		return nil
	case TypeError:
		return nil
	default:
		return errors.New("SignalingMessageInvalid", "unknown message type", "type", m.Type)
	}
}
