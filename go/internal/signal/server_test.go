// go/internal/signal/server_test.go
package signal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
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

// registryEntry mirrors the JSON shape returned by GET /registry.
type registryEntry struct {
	MachineID string `json:"machine_id"`
	Blob      string `json:"blob"`
}

// getRegistry fetches GET /registry?wallet=W against the httptest base URL and
// decodes the response into entries, asserting the status code.
func getRegistry(t *testing.T, base, wallet string, wantStatus int) []registryEntry {
	t.Helper()
	u := base + "/registry"
	if wallet != "" {
		u += "?wallet=" + wallet
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s: status %d want %d (body %q)", u, resp.StatusCode, wantStatus, string(body))
	}
	if wantStatus != http.StatusOK {
		return nil
	}
	var out []registryEntry
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode registry: %v", err)
	}
	return out
}

// registerAgentWithRegistry dials /agent/signal for owner|machine, waits for the
// ready frame, then publishes a TypeRegistry blob as its first message — exactly
// how a real agent rides its encrypted record on the live registration.
func registerAgentWithRegistry(t *testing.T, base, owner, machine, blob string) *websocket.Conn {
	t.Helper()
	c := dialJSON(t, wsURL(base, "/agent/signal", map[string]string{"owner_id": owner, "machine_id": machine}))
	if ready := readMsg(t, c); ready.Type != TypeReady {
		t.Fatalf("expected ready, got %q", ready.Type)
	}
	writeMsg(t, c, SignalMsg{Type: TypeRegistry, Registry: blob})
	return c
}

func sortEntries(e []registryEntry) {
	sort.Slice(e, func(i, j int) bool { return e[i].MachineID < e[j].MachineID })
}

func TestRegistryListsLiveAgents(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	const W = "wallet-W"
	const W2 = "wallet-W2"

	// Two live agents under W, each publishing its own opaque blob.
	a1 := registerAgentWithRegistry(t, srv.URL, W, "m1", "blob1")
	defer a1.CloseNow()
	a2 := registerAgentWithRegistry(t, srv.URL, W, "m2", "blob2")
	defer a2.CloseNow()
	// An agent under a different wallet must never be listed for W.
	other := registerAgentWithRegistry(t, srv.URL, W2, "m3", "blob3")
	defer other.CloseNow()

	// The blob publish rides the live connection asynchronously; poll briefly so
	// the read loop has stored both before we assert.
	var got []registryEntry
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got = getRegistry(t, srv.URL, W, http.StatusOK)
		if len(got) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	sortEntries(got)
	want := []registryEntry{{MachineID: "m1", Blob: "blob1"}, {MachineID: "m2", Blob: "blob2"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("registry for W = %+v, want %+v", got, want)
	}

	// W2's agent is isolated to W2 — never leaks into W's list.
	w2 := getRegistry(t, srv.URL, W2, http.StatusOK)
	if len(w2) != 1 || w2[0].MachineID != "m3" || w2[0].Blob != "blob3" {
		t.Fatalf("registry for W2 = %+v, want one m3/blob3 entry", w2)
	}

	// Disconnect m1 — it must drop from the list (in-memory soft-state, no
	// persistence). The blob lives only on the live agentConn.
	a1.Close(websocket.StatusNormalClosure, "")
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got = getRegistry(t, srv.URL, W, http.StatusOK)
		if len(got) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(got) != 1 || got[0].MachineID != "m2" || got[0].Blob != "blob2" {
		t.Fatalf("after m1 disconnect, registry for W = %+v, want only m2/blob2", got)
	}
}

func TestRegistryUnknownWalletIsEmptyArray(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	// A fresh Server holds no registry state: an unknown wallet returns [] (200),
	// never an error.
	u := srv.URL + "/registry?wallet=Unknown"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(body)); got != "[]" {
		t.Fatalf("unknown wallet body = %q, want []", got)
	}
}

func TestRegistryMissingWalletIsBadRequest(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	getRegistry(t, srv.URL, "", http.StatusBadRequest)
}
