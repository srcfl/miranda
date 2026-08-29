package netsim

// The netsim driver. It runs only inside the Docker harness (MIR_NETSIM=1) and
// is skipped everywhere else, so `go test ./...` stays hermetic and fast.
//
// Three entry points, selected with -test.run:
//
//	TestNetsimProvision — mints an owner identity and an agent keystore in the
//	                      shared state volume, pinning each to the other. This
//	                      replaces the interactive pairing handshake; the
//	                      material it writes is exactly what `mir pair` would
//	                      have produced.
//	TestNetsimScenario  — drives the real attach path for one scenario and
//	                      writes a JSON sample set to $NETSIM_RESULTS_DIR/raw.
//	TestNetsimReport    — folds every raw sample set into a markdown table.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/agent"
	"github.com/srcful/terminal-relay/go/internal/client"
	"github.com/srcful/terminal-relay/go/internal/identity"
	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// requireHarness keeps the driver inert outside the containers.
func requireHarness(t *testing.T) {
	t.Helper()
	if os.Getenv("MIR_NETSIM") != "1" {
		t.Skip("netsim: driver runs only inside the Docker harness (set MIR_NETSIM=1; see netsim/README.md)")
	}
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if n, err := strconv.Atoi(env(key, "")); err == nil {
		return n
	}
	return def
}

func envDur(key string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(env(key, "")); err == nil {
		return d
	}
	return def
}

func ms(d time.Duration) int64 { return d.Milliseconds() }

// ---------------------------------------------------------------------------
// provisioning
// ---------------------------------------------------------------------------

// TestNetsimProvision writes the pairing outcome straight into the shared state
// volume: a client owner identity, an agent keystore, the owner pin, the
// owner-signed registration authorization the relay demands, and the sealed
// registry record. Doing it here rather than over `mir pair` keeps the harness
// single-shot and deterministic; the attach path under test is untouched.
func TestNetsimProvision(t *testing.T) {
	requireHarness(t)

	clientDir := env("NETSIM_CLIENT_DIR", "/state/client")
	agentDir := env("NETSIM_AGENT_DIR", "/state/agent")
	signalURL := env("NETSIM_SIGNAL_URL", "http://10.88.0.10:8443")
	name := env("NETSIM_MACHINE_NAME", "netsim-box")

	id, err := client.LoadOrCreateIdentity(clientDir)
	if err != nil {
		t.Fatalf("client identity: %v", err)
	}
	if !id.HasRootedIdentity() {
		t.Fatal("client identity: no rooted owner secret")
	}
	cfg, err := agent.LoadOrInit(agentDir, name, signalURL)
	if err != nil {
		t.Fatalf("agent keystore: %v", err)
	}
	signer, err := id.Signer()
	if err != nil {
		t.Fatalf("owner signer: %v", err)
	}
	commitment, err := cfg.RegistrationCommitment()
	if err != nil {
		t.Fatalf("registration commitment: %v", err)
	}
	// The relay refuses an agent registration for a real base58 owner id unless
	// the agent presents this owner signature over machine_id + the SHA-256
	// commitment of its registration secret (signal.agentRegistrationAuthorized).
	regAuth := base64.StdEncoding.EncodeToString(
		signer.SignAuth(identity.RegistrationChallenge(cfg.MachineID, commitment)))

	m := client.Machine{Name: name, MachineID: cfg.MachineID, HostPubHex: cfg.HostPubHex, SignalURL: signalURL}
	blob, _, err := client.SealRegistryMachine(id, m)
	if err != nil {
		t.Fatalf("seal registry record: %v", err)
	}
	if err := agent.ProvisionOwner(agentDir, id.OwnerID, blob, regAuth); err != nil {
		t.Fatalf("provision owner on agent: %v", err)
	}
	if err := client.AddMachine(clientDir, m); err != nil {
		t.Fatalf("pin machine on client: %v", err)
	}
	t.Logf("provisioned owner=%s machine=%s (%s) via %s", id.OwnerID, m.Name, m.MachineID, signalURL)
}

// ---------------------------------------------------------------------------
// results
// ---------------------------------------------------------------------------

type sample struct {
	Rep       int    `json:"rep"`
	DialMS    int64  `json:"dial_ms"`             // Attach() start -> Noise session up
	AttachMS  int64  `json:"attach_ms"`           // Attach() start -> shell echoed our probe
	DetectMS  int64  `json:"detect_ms,omitempty"` // link flip -> client noticed the drop
	RedialMS  int64  `json:"redial_ms,omitempty"` // drop -> next session up (ReconnectNotify.OnResumed)
	ResumeMS  int64  `json:"resume_ms,omitempty"` // link flip -> session carries bytes again
	Continued bool   `json:"continued"`           // the pre-flip tmux job was still running after
	OK        bool   `json:"ok"`
	Err       string `json:"error,omitempty"`
}

type scenarioResult struct {
	Scenario  string   `json:"scenario"`
	Order     int      `json:"order"`
	AgentNAT  string   `json:"agent_nat"`
	ClientNAT string   `json:"client_nat"`
	ICE       string   `json:"ice"`
	Note      string   `json:"note"`
	Flip      bool     `json:"flip"`
	Expect    string   `json:"expect"` // "pass" or "fail"
	StartedAt string   `json:"started_at"`
	Samples   []sample `json:"samples"`
}

// ---------------------------------------------------------------------------
// the driver
// ---------------------------------------------------------------------------

// hbPeriod is how often the surviving tmux job prints a heartbeat. It bounds the
// granularity of the continuation check, not of resume_ms (which is measured on
// the first byte of any kind).
const hbPeriod = "0.2"

var hbRE = regexp.MustCompile(`HB_([0-9a-f]{8})_(\d+)`)

// tailCap bounds the rolling scan buffer. tmux repaints a whole pane on attach,
// so a few KB is plenty to find the newest heartbeat in.
const tailCap = 16 << 10

func TestNetsimScenario(t *testing.T) {
	requireHarness(t)

	res := scenarioResult{
		Scenario:  env("NETSIM_SCENARIO", "unnamed"),
		Order:     envInt("NETSIM_ORDER", 0),
		AgentNAT:  env("NETSIM_AGENT_NAT", "?"),
		ClientNAT: env("NETSIM_CLIENT_NAT", "?"),
		ICE:       env("NETSIM_ICE", "?"),
		Note:      env("NETSIM_NOTE", ""),
		Flip:      os.Getenv("NETSIM_FLIP") == "1",
		Expect:    env("NETSIM_EXPECT", "pass"),
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	reps := envInt("NETSIM_REPS", 3)
	clientDir := env("NETSIM_CLIENT_DIR", "/state/client")
	resultsDir := env("NETSIM_RESULTS_DIR", "/results")

	id, err := client.LoadOrCreateIdentity(clientDir)
	if err != nil {
		t.Fatalf("client identity: %v", err)
	}
	machines, err := client.ListMachines(clientDir)
	if err != nil || len(machines) == 0 {
		t.Fatalf("no pinned machine in %s (run TestNetsimProvision first): %v", clientDir, err)
	}
	m := machines[0]

	ice, err := client.ResolveICE(context.Background(), m.SignalURL, iceFallback())
	if err != nil {
		t.Fatalf("resolve ICE: %v", err)
	}
	t.Logf("scenario %s: machine=%s ice_servers=%d turn=%v", res.Scenario, m.Name, len(ice), hasTURN(ice))

	for rep := 1; rep <= reps; rep++ {
		s := runOnce(t, rep, m, id, ice, res.Flip)
		res.Samples = append(res.Samples, s)
		t.Logf("rep %d/%d: ok=%v dial=%dms attach=%dms detect=%dms redial=%dms resume=%dms continued=%v err=%s",
			rep, reps, s.OK, s.DialMS, s.AttachMS, s.DetectMS, s.RedialMS, s.ResumeMS, s.Continued, s.Err)
	}

	if err := writeResult(resultsDir, res); err != nil {
		t.Fatalf("write result: %v", err)
	}

	ok := 0
	for _, s := range res.Samples {
		if s.OK {
			ok++
		}
	}
	if res.Expect == "fail" {
		if ok > 0 {
			t.Logf("NOTE: %s was expected to fail but %d/%d reps succeeded — update the scenario's expectation",
				res.Scenario, ok, len(res.Samples))
		}
		return
	}
	if ok == 0 {
		t.Fatalf("scenario %s: every rep failed", res.Scenario)
	}
}

// runOnce drives one full measurement: attach, prove a live shell, optionally
// flip the client's uplink, and measure how long the session takes to carry
// bytes again. It runs on top of the production ReconnectLoop with the
// production policy, so the numbers are the ones a user would feel.
func runOnce(t *testing.T, rep int, m client.Machine, id *client.Identity, ice []peer.ICEServer, flip bool) sample {
	t.Helper()
	budget := envDur("NETSIM_REP_BUDGET", 90*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	st := newRunState(flip)
	out := sample{Rep: rep}

	dial := func(ctx context.Context) (peer.MsgConn, *noise.Session, func(), error) {
		start := time.Now()
		mc, sess, cleanup, err := client.Attach(ctx, m, id, ice, relayOnly())
		if err == nil {
			st.noteDial(start)
		}
		return mc, sess, cleanup, err
	}

	// Everything except the failure budget stays at the production default, so
	// the timings are the ones a user feels. A scenario that is expected to fail
	// shortens the budget (NETSIM_MAX_FAILURES=1) rather than waiting out seven
	// 20-second ICE attempts.
	policy := client.ReconnectPolicy{
		MaxFailures: envInt("NETSIM_MAX_FAILURES", 0),
		Notify: client.ReconnectNotify{
			OnReconnecting: func(attempt int) { t.Logf("  reconnecting (attempt %d)", attempt) },
			OnResumed:      func(outage time.Duration) { st.noteRedial(outage) },
			OnGaveUp:       func(failures int, lastErr error) { t.Logf("  gave up after %d: %v", failures, lastErr) },
		},
	}

	go st.control(ctx, cancel, t)

	err := client.ReconnectLoopWith(ctx, policy, dial, st.run)

	st.fill(&out)
	switch {
	case st.completed():
		out.OK = true
	case err != nil:
		out.Err = err.Error()
	case ctx.Err() != nil:
		out.Err = "timed out before the scenario completed"
	default:
		out.Err = "reconnect loop ended early"
	}
	if out.Err != "" && st.lastErr() != "" {
		out.Err += " (" + st.lastErr() + ")"
	}
	return out
}

// runState carries one rep's timeline across the reconnect loop's callbacks.
type runState struct {
	flip bool

	mu       sync.Mutex
	sessions int
	snd      *sender
	dialMS   int64
	redialMS int64

	firstProbe time.Time // shell echoed the attach probe
	dialStart  time.Time
	flipAt     time.Time
	dropAt     time.Time
	resumeAt   time.Time

	hbAtFlip  int64
	lastHB    int64
	continued bool
	failure   string
	doneOK    bool

	runID string

	attachedC  chan struct{}
	resumedC   chan struct{}
	continuedC chan struct{}
	hbC        chan struct{}

	attachedOnce, resumedOnce, continuedOnce, hbOnce sync.Once
}

func newRunState(flip bool) *runState {
	return &runState{
		flip:       flip,
		runID:      fmt.Sprintf("%08x", time.Now().UnixNano()&0xffffffff),
		attachedC:  make(chan struct{}),
		resumedC:   make(chan struct{}),
		continuedC: make(chan struct{}),
		hbC:        make(chan struct{}),
	}
}

func (s *runState) noteDial(start time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dialStart.IsZero() {
		s.dialStart = start
		s.dialMS = ms(time.Since(start))
	}
}

func (s *runState) noteRedial(outage time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.redialMS == 0 {
		s.redialMS = ms(outage)
	}
}

func (s *runState) lastErr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failure
}

func (s *runState) completed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.doneOK
}

func (s *runState) fill(out *sample) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out.DialMS = s.dialMS
	out.RedialMS = s.redialMS
	out.Continued = s.continued
	if !s.dialStart.IsZero() && !s.firstProbe.IsZero() {
		out.AttachMS = ms(s.firstProbe.Sub(s.dialStart))
	}
	if !s.flipAt.IsZero() && !s.dropAt.IsZero() {
		out.DetectMS = ms(s.dropAt.Sub(s.flipAt))
	}
	if !s.flipAt.IsZero() && !s.resumeAt.IsZero() {
		out.ResumeMS = ms(s.resumeAt.Sub(s.flipAt))
	}
}

// run is the SessionRun handed to the production reconnect loop. Session 1 sends
// the attach probe and then the surviving heartbeat job; every session feeds
// observations back to the controller.
func (s *runState) run(ctx context.Context, mc peer.MsgConn, sess *noise.Session) error {
	snd := newSender(mc, sess)
	s.mu.Lock()
	s.sessions++
	idx := s.sessions
	s.snd = snd
	s.mu.Unlock()

	if idx == 1 {
		if err := snd.send(noise.EncodeResize(120, 40)); err != nil {
			return err
		}
		// The empty shell quotes make the terminal's echo of the typed line differ
		// from the line the shell prints, so the token can only match real output.
		if err := snd.send(noise.EncodeData([]byte(`echo NETSIM""_PROBE_` + s.runID + "\n"))); err != nil {
			return err
		}
	}

	probe := []byte("NETSIM_PROBE_" + s.runID)
	var tail []byte
	for {
		ct, err := mc.Recv(ctx)
		if err != nil {
			s.noteDrop()
			return err
		}
		pt, err := sess.Decrypt(ct)
		if err != nil {
			s.noteDrop()
			return err
		}
		typ, payload, err := noise.DecodeFrame(pt)
		if err != nil || typ != noise.FrameData {
			continue
		}
		if idx > 1 {
			s.noteResume()
		}
		tail = appendTail(tail, payload)
		if bytes.Contains(tail, probe) {
			s.noteAttached()
		}
		s.scanHeartbeat(tail)
	}
}

func (s *runState) noteAttached() {
	s.mu.Lock()
	if s.firstProbe.IsZero() {
		s.firstProbe = time.Now()
	}
	s.mu.Unlock()
	s.attachedOnce.Do(func() { close(s.attachedC) })
}

func (s *runState) noteResume() {
	s.mu.Lock()
	if s.resumeAt.IsZero() && !s.flipAt.IsZero() {
		s.resumeAt = time.Now()
	}
	s.mu.Unlock()
	s.resumedOnce.Do(func() { close(s.resumedC) })
}

func (s *runState) noteDrop() {
	s.mu.Lock()
	if s.dropAt.IsZero() && !s.flipAt.IsZero() {
		s.dropAt = time.Now()
	}
	s.mu.Unlock()
}

// scanHeartbeat records the highest heartbeat counter seen so far. A counter
// above the one observed at flip time proves the tmux job ran right through the
// outage — that is the continuation the beta gate asks for.
func (s *runState) scanHeartbeat(tail []byte) {
	matches := hbRE.FindAllSubmatch(tail, -1)
	var best int64
	for _, mm := range matches {
		if string(mm[1]) != s.runID {
			continue
		}
		if n, err := strconv.ParseInt(string(mm[2]), 10, 64); err == nil && n > best {
			best = n
		}
	}
	if best == 0 {
		return
	}
	s.mu.Lock()
	if best > s.lastHB {
		s.lastHB = best
	}
	flipped := !s.flipAt.IsZero()
	crossed := flipped && best > s.hbAtFlip
	if crossed {
		s.continued = true
	}
	s.mu.Unlock()
	s.hbOnce.Do(func() { close(s.hbC) })
	if crossed {
		s.continuedOnce.Do(func() { close(s.continuedC) })
	}
}

// control owns the rep's timeline. It waits for the shell to answer, starts the
// heartbeat, optionally flips the uplink after a healthy hold, then declares the
// rep done and cancels the loop.
func (s *runState) control(ctx context.Context, cancel context.CancelFunc, t *testing.T) {
	defer cancel()
	fail := func(msg string) {
		s.mu.Lock()
		if s.failure == "" {
			s.failure = msg
		}
		s.mu.Unlock()
	}

	select {
	case <-s.attachedC:
	case <-ctx.Done():
		fail("shell never echoed the attach probe")
		return
	}

	// A background job in the tmux session: it must outlive the network flip.
	hb := fmt.Sprintf(`i=0; while :; do i=$((i+1)); echo HB""_%s""_$i; sleep %s; done &`+"\n", s.runID, hbPeriod)
	if err := s.send([]byte(hb)); err != nil {
		fail("start heartbeat: " + err.Error())
		return
	}
	select {
	case <-s.hbC:
	case <-time.After(10 * time.Second):
		fail("heartbeat job never produced output")
		return
	case <-ctx.Done():
		fail("heartbeat job never produced output")
		return
	}

	if !s.flip {
		// Non-flip scenario: hold the session briefly to prove it is stable, then
		// stop. Nothing else to measure.
		select {
		case <-time.After(envDur("NETSIM_HOLD", 2*time.Second)):
			s.markDone()
		case <-ctx.Done():
			fail("session did not stay up")
		}
		return
	}

	// Hold longer than ReconnectPolicy.MinHealthy (5s) so the drop counts as a
	// healthy session ending, which is what a real Wi-Fi -> cellular flip is.
	select {
	case <-time.After(envDur("NETSIM_FLIP_AFTER", 7*time.Second)):
	case <-ctx.Done():
		fail("session did not survive to the flip")
		return
	}

	s.mu.Lock()
	s.hbAtFlip = s.lastHB
	s.flipAt = time.Now()
	s.mu.Unlock()
	if out, err := flipUplink(ctx); err != nil {
		fail("flip uplink: " + err.Error() + ": " + out)
		return
	} else if strings.TrimSpace(out) != "" {
		t.Logf("  flip: %s", strings.TrimSpace(out))
	}

	select {
	case <-s.resumedC:
	case <-ctx.Done():
		fail("session never carried bytes again after the flip")
		return
	}
	select {
	case <-s.continuedC:
		s.markDone()
	case <-time.After(envDur("NETSIM_CONTINUE_WAIT", 10*time.Second)):
		fail("session resumed but the pre-flip job was gone")
	case <-ctx.Done():
		fail("session resumed but the pre-flip job was gone")
	}
}

func (s *runState) markDone() {
	s.mu.Lock()
	s.doneOK = true
	s.mu.Unlock()
}

func (s *runState) send(b []byte) error {
	s.mu.Lock()
	snd := s.snd
	s.mu.Unlock()
	if snd == nil {
		return fmt.Errorf("no live session")
	}
	return snd.send(noise.EncodeData(b))
}

// flipUplink runs the container-side script that swaps the client's default
// route onto its other uplink and downs the old one — the harness's stand-in for
// walking out of Wi-Fi range onto cellular.
func flipUplink(ctx context.Context) (string, error) {
	script := env("NETSIM_FLIP_CMD", "/usr/local/bin/netsim-flip.sh")
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, script).CombinedOutput()
	return string(out), err
}

// sender mirrors client's internal sender: Encrypt mutates a nonce counter, so
// every frame for one session must go through a single lock.
type sender struct {
	mc   peer.MsgConn
	sess *noise.Session
	mu   sync.Mutex
}

func newSender(mc peer.MsgConn, sess *noise.Session) *sender {
	return &sender{mc: mc, sess: sess}
}

func (s *sender) send(framed []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ct, err := s.sess.Encrypt(framed)
	if err != nil {
		return err
	}
	return s.mc.Send(ct)
}

func appendTail(tail, next []byte) []byte {
	tail = append(tail, next...)
	if len(tail) > tailCap {
		tail = append(tail[:0], tail[len(tail)-tailCap:]...)
	}
	return tail
}

func hasTURN(servers []peer.ICEServer) bool {
	for _, s := range servers {
		if s.Username != "" || s.Credential != "" {
			return true
		}
	}
	return false
}

func iceFallback() []string {
	if v := env("MIR_STUN", ""); v != "" {
		return strings.Split(v, ",")
	}
	return nil
}

func relayOnly() bool { return os.Getenv("NETSIM_RELAY_ONLY") == "1" }

func writeResult(dir string, res scenarioResult) error {
	raw := filepath.Join(dir, "raw")
	if err := os.MkdirAll(raw, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(raw, res.Scenario+".json"), append(data, '\n'), 0o644)
}

// ---------------------------------------------------------------------------
// report
// ---------------------------------------------------------------------------

func TestNetsimReport(t *testing.T) {
	requireHarness(t)
	dir := env("NETSIM_RESULTS_DIR", "/results")
	entries, err := filepath.Glob(filepath.Join(dir, "raw", "*.json"))
	if err != nil {
		t.Fatalf("glob results: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("no raw results in %s/raw", dir)
	}
	var all []scenarioResult
	for _, path := range entries {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var r scenarioResult
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		all = append(all, r)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Order != all[j].Order {
			return all[i].Order < all[j].Order
		}
		return all[i].Scenario < all[j].Scenario
	})

	md := renderReport(all)
	out := filepath.Join(dir, "results.md")
	if err := os.WriteFile(out, []byte(md), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	fmt.Print(md)
	t.Logf("wrote %s", out)
}

func renderReport(all []scenarioResult) string {
	var b strings.Builder
	b.WriteString("# netsim NAT matrix\n\n")
	b.WriteString("Generated by `netsim/run.sh` (see `netsim/README.md` for what each NAT approximates).\n")
	if len(all) > 0 {
		b.WriteString("Run started " + all[0].StartedAt + ".\n")
	}
	b.WriteString("\n")
	b.WriteString("| Scenario | Agent NAT | Client NAT | ICE | Attach p50 / max | Resume p50 / max | Continuation | Runs |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|\n")

	totalRuns, totalOK, flipRuns, flipContinued := 0, 0, 0, 0
	for _, r := range all {
		var attach, resume []int64
		ok, cont := 0, 0
		for _, s := range r.Samples {
			if s.OK {
				ok++
				if s.AttachMS > 0 {
					attach = append(attach, s.AttachMS)
				}
				if s.ResumeMS > 0 {
					resume = append(resume, s.ResumeMS)
				}
			}
			if s.Continued {
				cont++
			}
		}
		// A scenario written to fail is a documented limit, not a data point in
		// the continuation rate, so it is counted and reported separately.
		if r.Expect != "fail" {
			totalRuns += len(r.Samples)
			totalOK += ok
		}
		if r.Flip {
			flipRuns += len(r.Samples)
			flipContinued += cont
		}
		contCell := "—"
		if r.Flip {
			contCell = fmt.Sprintf("%d/%d", cont, len(r.Samples))
		} else if ok > 0 {
			contCell = "n/a"
		}
		runs := fmt.Sprintf("%d/%d ok", ok, len(r.Samples))
		if r.Expect == "fail" {
			runs += " *(expected fail)*"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | %s | %s | %s |\n",
			r.Scenario, r.AgentNAT, r.ClientNAT, r.ICE,
			pairCell(attach), pairCell(resume), contCell, runs)
	}
	b.WriteString("\n")
	if totalRuns > 0 {
		fmt.Fprintf(&b, "**%d/%d reps connected (%.0f%%)** across the scenarios that should connect.\n",
			totalOK, totalRuns, 100*float64(totalOK)/float64(totalRuns))
	}
	if flipRuns > 0 {
		fmt.Fprintf(&b, "**%d/%d network flips kept the session's work alive (%.0f%%).**\n",
			flipContinued, flipRuns, 100*float64(flipContinued)/float64(flipRuns))
	}
	for _, r := range all {
		if r.Expect == "fail" {
			fmt.Fprintf(&b, "\n`%s` is excluded from those rates: it is written to fail, and does.\n", r.Scenario)
		}
	}
	b.WriteString("\n## Notes\n\n")
	for _, r := range all {
		if r.Note != "" {
			fmt.Fprintf(&b, "- `%s` — %s\n", r.Scenario, r.Note)
		}
	}
	b.WriteString("\n## Failures\n\n")
	failures := 0
	for _, r := range all {
		tag := ""
		if r.Expect == "fail" {
			tag = " *(expected)*"
		}
		for _, s := range r.Samples {
			if !s.OK {
				fmt.Fprintf(&b, "- `%s` rep %d%s: %s\n", r.Scenario, s.Rep, tag, s.Err)
				failures++
			}
		}
	}
	if failures == 0 {
		b.WriteString("None.\n")
	}
	b.WriteString("\n### Measurement definitions\n\n")
	b.WriteString("- **attach** — `client.Attach` start until the agent's shell echoes our probe (a full round trip through the PTY).\n")
	b.WriteString("- **resume** — the moment the client's uplink is swapped until the resumed session carries a byte again.\n")
	b.WriteString("- **continuation** — the background job started before the flip was still running after it, proving tmux held the shell.\n")
	return b.String()
}

func pairCell(v []int64) string {
	if len(v) == 0 {
		return "—"
	}
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	return fmt.Sprintf("%d / %d ms", v[len(v)/2], v[len(v)-1])
}
