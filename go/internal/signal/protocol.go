// go/internal/signal/protocol.go
package signal

import "encoding/json"

// Message types on the signaling channel. SDP carries candidates (non-trickle).
const (
	TypeReady  = "ready"  // server -> agent: registered
	TypeAttach = "attach" // server -> agent: a browser wants you; session id attached
	TypeOffer  = "offer"  // browser -> server -> agent (tagged with session)
	TypeAnswer = "answer" // agent -> server -> browser
	TypeError  = "error"  // server -> peer: e.g. machine offline
	TypeClose  = "close"  // either way: session ended

	TypeRegistry = "registry" // agent -> relay: publish my (opaque) device registry blob
)

// SignalMsg is the only thing that crosses the signaling WSS. It never contains
// terminal data — only WebRTC SDP and routing. Session is set on agent-facing
// messages so one agent connection can serve multiple browser sessions.
type SignalMsg struct {
	Type     string `json:"type"`
	Session  string `json:"session,omitempty"`
	SDP      string `json:"sdp,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Binding  string `json:"binding,omitempty"`  // opaque wallet-binding record; relay forwards, never reads
	Registry string `json:"registry,omitempty"` // opaque encrypted device record; relay holds + serves, never reads
}

func (m SignalMsg) encode() ([]byte, error) { return json.Marshal(m) }

func decodeSignal(b []byte) (SignalMsg, error) {
	var m SignalMsg
	err := json.Unmarshal(b, &m)
	return m, err
}
