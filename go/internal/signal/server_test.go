// go/internal/signal/server_test.go
package signal

import (
	"context"
	"net/http/httptest"
	"strings"
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
	writeRaw(t, c, data)
}

func writeRaw(t *testing.T, c *websocket.Conn, data []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, data); err != nil {
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

func assertNoSignal(t *testing.T, c *websocket.Conn, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err == nil {
		raw := string(data)
		if len(raw) > 128 {
			raw = raw[:128]
		}
		t.Fatalf("unexpected signal message: %s", raw)
	}
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("signal read ended before timeout: %v", err)
	}
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

func TestOfferBindingReachesAgentVerbatim(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	agent := dialJSON(t, wsURL(srv.URL, "/agent/signal", map[string]string{"owner_id": "o", "machine_id": "m"}))
	if ready := readMsg(t, agent); ready.Type != TypeReady {
		t.Fatalf("expected ready, got %q", ready.Type)
	}

	browser := dialJSON(t, wsURL(srv.URL, "/attach", map[string]string{"owner_id": "o", "machine_id": "m"}))
	attach := readMsg(t, agent)
	if attach.Type != TypeAttach || attach.Session == "" {
		t.Fatalf("expected attach with session, got %+v", attach)
	}

	// Browser sends an offer carrying an opaque wallet-binding record. The relay
	// must forward it verbatim without interpreting it.
	const binding = "OPAQUE-WALLET-BINDING-RECORD"
	writeMsg(t, browser, SignalMsg{Type: TypeOffer, SDP: "OFFER-SDP", Binding: binding})
	gotOffer := readMsg(t, agent)
	if gotOffer.Type != TypeOffer || gotOffer.SDP != "OFFER-SDP" || gotOffer.Session != attach.Session {
		t.Fatalf("agent got wrong offer: %+v", gotOffer)
	}
	if gotOffer.Binding != binding {
		t.Fatalf("binding not forwarded verbatim: got %q want %q", gotOffer.Binding, binding)
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

func TestSignalMessageSizeLimit(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	agent := dialJSON(t, wsURL(srv.URL, "/agent/signal", map[string]string{"owner_id": "o", "machine_id": "m"}))
	if ready := readMsg(t, agent); ready.Type != TypeReady {
		t.Fatalf("expected ready, got %q", ready.Type)
	}

	browser := dialJSON(t, wsURL(srv.URL, "/attach", map[string]string{"owner_id": "o", "machine_id": "m"}))
	attach := readMsg(t, agent)
	if attach.Type != TypeAttach || attach.Session == "" {
		t.Fatalf("expected attach with session, got %+v", attach)
	}

	allowedSDP := strings.Repeat("a", 16<<10)
	writeMsg(t, browser, SignalMsg{Type: TypeOffer, SDP: allowedSDP})
	gotOffer := readMsg(t, agent)
	if gotOffer.Type != TypeOffer || gotOffer.SDP != allowedSDP || gotOffer.Session != attach.Session {
		t.Fatalf("agent got wrong large offer: type=%q sdp_len=%d session=%q", gotOffer.Type, len(gotOffer.SDP), gotOffer.Session)
	}

	oversizeSDP := strings.Repeat("b", maxSignalMessageBytes)
	writeMsg(t, browser, SignalMsg{Type: TypeOffer, SDP: oversizeSDP})
	assertNoSignal(t, agent, 250*time.Millisecond)
}

func TestAttachCapacityPerAgentFailsFast(t *testing.T) {
	s := New()
	s.maxAgentSessions = 1
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	agent := dialJSON(t, wsURL(srv.URL, "/agent/signal", map[string]string{"owner_id": "o", "machine_id": "m"}))
	if ready := readMsg(t, agent); ready.Type != TypeReady {
		t.Fatalf("expected ready, got %q", ready.Type)
	}

	first := dialJSON(t, wsURL(srv.URL, "/attach", map[string]string{"owner_id": "o", "machine_id": "m"}))
	defer first.CloseNow()
	if attach := readMsg(t, agent); attach.Type != TypeAttach || attach.Session == "" {
		t.Fatalf("expected first attach, got %+v", attach)
	}

	second := dialJSON(t, wsURL(srv.URL, "/attach", map[string]string{"owner_id": "o", "machine_id": "m"}))
	defer second.CloseNow()
	if msg := readMsg(t, second); msg.Type != TypeError || msg.Reason != "server capacity reached" {
		t.Fatalf("expected capacity error, got %+v", msg)
	}
	assertNoSignal(t, agent, 250*time.Millisecond)
}
