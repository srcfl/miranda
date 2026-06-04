// go/internal/signal/server_test.go
package signal

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func wsURL(base, path string, q map[string]string) string {
	u := "ws" + base[len("http"):] + path
	sep := "?"
	for k, v := range q {
		u += sep + k + "=" + v
		sep = "&"
	}
	return u
}

func dialJSON(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.Dial(context.Background(), url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	return c
}

func writeMsg(t *testing.T, c *websocket.Conn, m SignalMsg) {
	t.Helper()
	data, _ := m.encode()
	if err := c.Write(context.Background(), websocket.MessageText, data); err != nil {
		t.Fatal(err)
	}
}

func readMsg(t *testing.T, c *websocket.Conn) SignalMsg {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	m, err := decodeSignal(data)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestOfferReachesAgentAnswerReachesBrowser(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	agent := dialJSON(t, wsURL(srv.URL, "/agent/signal", map[string]string{"owner_id": "o", "machine_id": "m"}))
	if ready := readMsg(t, agent); ready.Type != TypeReady {
		t.Fatalf("expected ready, got %q", ready.Type)
	}

	browser := dialJSON(t, wsURL(srv.URL, "/attach", map[string]string{"owner_id": "o", "machine_id": "m"}))

	// Agent is notified of the attach with a session id.
	attach := readMsg(t, agent)
	if attach.Type != TypeAttach || attach.Session == "" {
		t.Fatalf("expected attach with session, got %+v", attach)
	}

	// Browser sends an offer; server tags it with the session toward the agent.
	writeMsg(t, browser, SignalMsg{Type: TypeOffer, SDP: "OFFER-SDP"})
	gotOffer := readMsg(t, agent)
	if gotOffer.Type != TypeOffer || gotOffer.SDP != "OFFER-SDP" || gotOffer.Session != attach.Session {
		t.Fatalf("agent got wrong offer: %+v", gotOffer)
	}

	// Agent answers (tagged with session); browser receives it untagged.
	writeMsg(t, agent, SignalMsg{Type: TypeAnswer, Session: attach.Session, SDP: "ANSWER-SDP"})
	gotAnswer := readMsg(t, browser)
	if gotAnswer.Type != TypeAnswer || gotAnswer.SDP != "ANSWER-SDP" {
		t.Fatalf("browser got wrong answer: %+v", gotAnswer)
	}
}

func TestAttachOfflineMachineGetsError(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	browser := dialJSON(t, wsURL(srv.URL, "/attach", map[string]string{"owner_id": "x", "machine_id": "y"}))
	m := readMsg(t, browser)
	if m.Type != TypeError {
		t.Fatalf("expected error for offline machine, got %+v", m)
	}
}
