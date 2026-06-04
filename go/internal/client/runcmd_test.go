// go/internal/client/runcmd_test.go
package client

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

func TestRunCommandStreamsShellOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	aPriv, aPub, _ := noise.GenerateStatic()
	cPriv, cPub, _ := noise.GenerateStatic()
	clientMC, agentMC := peer.Pipe()

	// Fake agent: Noise responder, echoes DATA back (like a shell echoing input).
	go func() {
		sess, err := peer.RunResponder(ctx, agentMC, aPriv, cPub)
		if err != nil {
			return
		}
		for {
			ct, err := agentMC.Recv(ctx)
			if err != nil {
				return
			}
			pt, err := sess.Decrypt(ct)
			if err != nil {
				return
			}
			typ, payload, _ := noise.DecodeFrame(pt)
			if typ == noise.FrameData {
				reply, _ := sess.Encrypt(noise.EncodeData(payload))
				_ = agentMC.Send(reply)
			}
		}
	}()

	sess, err := peer.RunInitiator(ctx, clientMC, cPriv, aPub)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := RunCommand(ctx, clientMC, sess, "echo RUN_OK", 1500*time.Millisecond, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("RUN_OK")) {
		t.Fatalf("output missing command echo; got %q", out.String())
	}
}
