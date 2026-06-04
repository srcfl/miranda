// go/internal/client/sender_test.go
package client

import (
	"context"
	"sync"
	"testing"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// Run with -race: concurrent sends through one sender must not race the Noise
// nonce, and the peer must decrypt every frame in order without auth failures.
func TestSenderSerializesConcurrentEncrypts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	aPriv, aPub, _ := noise.GenerateStatic()
	bPriv, bPub, _ := noise.GenerateStatic()
	clientMC, agentMC := peer.Pipe()

	const N = 200
	got := make(chan int, N)
	go func() {
		sess, err := peer.RunResponder(ctx, agentMC, aPriv, bPub)
		if err != nil {
			return
		}
		for i := 0; i < N; i++ {
			ct, err := agentMC.Recv(ctx)
			if err != nil {
				return
			}
			pt, err := sess.Decrypt(ct) // single decrypter: must never auth-fail
			if err != nil {
				t.Errorf("decrypt failed at %d: %v", i, err)
				return
			}
			_, payload, _ := noise.DecodeFrame(pt)
			got <- len(payload)
		}
	}()

	sess, err := peer.RunInitiator(ctx, clientMC, bPriv, aPub)
	if err != nil {
		t.Fatal(err)
	}
	s := newSender(clientMC, sess)

	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.send(noise.EncodeData([]byte("x")))
		}()
	}
	wg.Wait()
	for i := 0; i < N; i++ {
		<-got // all N decrypted successfully (no nonce reuse / auth failure)
	}
}
