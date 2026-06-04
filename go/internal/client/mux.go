// go/internal/client/mux.go
package client

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// DefaultPrefix is the switch key (Ctrl-]). Press it, then: a digit 1-9 to focus
// that machine, 'n' for next, 'q' to quit, or the prefix again to send a literal.
const DefaultPrefix byte = 0x1d

// MuxSession is one attached machine.
type MuxSession struct {
	Name string
	MC   peer.MsgConn
	Sess *noise.Session
	snd  *sender
}

// Mux multiplexes several machine sessions onto one local terminal: only the
// focused machine's output reaches the screen; keystrokes go to the focused
// machine; a prefix hotkey switches focus.
type Mux struct {
	sessions []*MuxSession
	out      io.Writer
	prefix   byte

	mu    sync.Mutex // guards focus, size, dead, and writes to out
	focus int
	size  Size
	dead  []bool

	quit     chan struct{}
	quitOnce sync.Once
}

func NewMux(sessions []*MuxSession, out io.Writer, prefix byte, initial Size) *Mux {
	if prefix == 0 {
		prefix = DefaultPrefix
	}
	for _, s := range sessions {
		s.snd = newSender(s.MC, s.Sess)
	}
	return &Mux{
		sessions: sessions,
		out:      out,
		prefix:   prefix,
		size:     initial,
		dead:     make([]bool, len(sessions)),
		quit:     make(chan struct{}),
	}
}

// Run drives the mux until quit, ctx cancel, or all machines disconnect.
func (m *Mux) Run(ctx context.Context, in io.Reader, resizes <-chan Size) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for i := range m.sessions {
		_ = m.sessions[i].snd.send(noise.EncodeResize(m.size.Cols, m.size.Rows))
	}
	m.setTitle(m.sessions[m.focus].Name)

	for i := range m.sessions {
		go m.readSession(ctx, i)
	}
	go m.resizeLoop(ctx, resizes)
	return m.readStdin(ctx, in)
}

func (m *Mux) readSession(ctx context.Context, i int) {
	s := m.sessions[i]
	for {
		ct, err := s.MC.Recv(ctx)
		if err != nil {
			m.onSessionEnd(i)
			return
		}
		pt, err := s.Sess.Decrypt(ct)
		if err != nil {
			m.onSessionEnd(i)
			return
		}
		typ, payload, err := noise.DecodeFrame(pt)
		if err != nil {
			continue
		}
		if typ == noise.FrameData {
			m.mu.Lock()
			if i == m.focus {
				_, _ = m.out.Write(payload)
			}
			m.mu.Unlock()
		}
	}
}

func (m *Mux) resizeLoop(ctx context.Context, resizes <-chan Size) {
	for {
		select {
		case sz := <-resizes:
			m.mu.Lock()
			m.size = sz
			f := m.focus
			m.mu.Unlock()
			_ = m.sessions[f].snd.send(noise.EncodeResize(sz.Cols, sz.Rows))
		case <-ctx.Done():
			return
		case <-m.quit:
			return
		}
	}
}

func (m *Mux) readStdin(ctx context.Context, in io.Reader) error {
	buf := make([]byte, 4096)
	armed := false
	for {
		n, err := in.Read(buf)
		// Batch consecutive non-command bytes into one DATA frame so the focused
		// machine receives a coherent payload (the prefix is still parsed byte by
		// byte, so a chunk may produce several runs).
		run := make([]byte, 0, n)
		flush := func() {
			if len(run) > 0 {
				m.forward(run)
				run = run[:0]
			}
		}
		for k := 0; k < n; k++ {
			b := buf[k]
			if armed {
				armed = false
				flush()
				m.command(b)
				continue
			}
			if b == m.prefix {
				armed = true
				flush()
				continue
			}
			run = append(run, b)
		}
		flush()
		select {
		case <-m.quit:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err != nil {
			return err
		}
	}
}

func (m *Mux) forward(b []byte) {
	m.mu.Lock()
	f := m.focus
	m.mu.Unlock()
	_ = m.sessions[f].snd.send(noise.EncodeData(b))
}

func (m *Mux) command(b byte) {
	switch {
	case b >= '1' && b <= '9':
		m.switchTo(int(b - '1'))
	case b == 'n':
		m.mu.Lock()
		next := m.nextLiveLocked()
		m.mu.Unlock()
		if next >= 0 {
			m.switchTo(next)
		}
	case b == 'q':
		m.quitOnce.Do(func() { close(m.quit) })
	case b == m.prefix:
		m.mu.Lock()
		f := m.focus
		m.mu.Unlock()
		_ = m.sessions[f].snd.send(noise.EncodeData([]byte{m.prefix}))
	}
}

func (m *Mux) switchTo(idx int) {
	m.mu.Lock()
	if idx < 0 || idx >= len(m.sessions) || m.dead[idx] || idx == m.focus {
		m.mu.Unlock()
		return
	}
	m.focus = idx
	size := m.size
	_, _ = io.WriteString(m.out, "\x1b[2J\x1b[H") // clear + home, under lock with other out writes
	m.mu.Unlock()

	m.setTitle(m.sessions[idx].Name)
	// Nudge the newly-focused machine's tmux to redraw the current screen.
	_ = m.sessions[idx].snd.send(noise.EncodeResize(size.Cols, size.Rows))
}

func (m *Mux) onSessionEnd(i int) {
	m.mu.Lock()
	if m.dead[i] {
		m.mu.Unlock()
		return
	}
	m.dead[i] = true
	fmt.Fprintf(m.out, "\r\n[tr] %s disconnected\r\n", m.sessions[i].Name)
	wasFocus := i == m.focus
	next := m.nextLiveLocked()
	m.mu.Unlock()

	if next < 0 {
		m.quitOnce.Do(func() { close(m.quit) })
		return
	}
	if wasFocus {
		m.switchTo(next)
	}
}

// nextLiveLocked returns the next non-dead session after focus (wrapping), or -1.
func (m *Mux) nextLiveLocked() int {
	for off := 1; off <= len(m.sessions); off++ {
		j := (m.focus + off) % len(m.sessions)
		if !m.dead[j] {
			return j
		}
	}
	return -1
}

func (m *Mux) setTitle(name string) {
	m.mu.Lock()
	_, _ = io.WriteString(m.out, "\x1b]0;tr: "+name+"\x07")
	m.mu.Unlock()
}
