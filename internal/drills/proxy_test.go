package drills

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
)

// proxy is a TCP relay that can be told to stop delivering bytes in one
// direction without closing anything.
//
// It exists because some session failures cannot be produced from either
// application. A venue that has stopped responding at the transport layer is
// not something an Application callback can simulate — heartbeats are generated
// by the engine, below the application, so an Application cannot decline to
// send one. Freezing the wire is the only honest way to show what an engine
// does when its peer goes quiet, which is drill 02's TestRequest.
type proxy struct {
	listener net.Listener
	target   string

	// frozen suppresses target-to-client delivery. Bytes are read from the
	// target and discarded, so the connection stays open and the target sees a
	// perfectly healthy peer — exactly the asymmetry that makes this failure
	// hard to reason about from one side.
	frozen atomic.Bool

	wg     sync.WaitGroup
	closed atomic.Bool
}

func startProxy(t *testing.T, targetPort int) *proxy {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}

	p := &proxy{listener: ln, target: netJoin(targetPort)}
	t.Cleanup(p.Close)

	go p.accept()
	return p
}

func (p *proxy) Port() int { return p.listener.Addr().(*net.TCPAddr).Port }

// Freeze stops delivering the target's bytes to the client.
func (p *proxy) Freeze() { p.frozen.Store(true) }

// Thaw resumes delivery. Anything the target sent while frozen is gone — which
// is realistic: a peer that stopped talking to you did not buffer it either.
func (p *proxy) Thaw() { p.frozen.Store(false) }

func (p *proxy) Close() {
	if p.closed.Swap(true) {
		return
	}
	p.listener.Close()
	p.wg.Wait()
}

func (p *proxy) accept() {
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}

		target, err := net.Dial("tcp", p.target)
		if err != nil {
			client.Close()
			continue
		}

		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.pipe(client, target)
		}()
	}
}

func (p *proxy) pipe(client, target net.Conn) {
	defer client.Close()
	defer target.Close()

	done := make(chan struct{}, 2)

	// client -> target, always delivered
	go func() {
		_, _ = io.Copy(target, client)
		done <- struct{}{}
	}()

	// target -> client, dropped while frozen
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := target.Read(buf)
			if n > 0 && !p.frozen.Load() {
				if _, werr := client.Write(buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		done <- struct{}{}
	}()

	<-done
}

func netJoin(port int) string {
	return net.JoinHostPort("127.0.0.1", itoa(port))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
