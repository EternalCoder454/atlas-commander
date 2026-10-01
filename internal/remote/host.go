package remote

import (
	"net"
	"strconv"
	"sync"
	"time"
)

// Status is what the settings page shows about phone access.
type Status struct {
	On   bool
	Port int
	// Listening is the "host:port" to show ("192.168.1.5:47821"), "" while off.
	Listening string
	// Link is the pairing link, "" while off.
	Link string
	// Err is a plain sentence when phone access is on but not working.
	Err string
}

// Host owns the Server for the app: it turns phone access on and off while
// Commander runs, and reports the result. Methods may be called from any
// goroutine; Status never blocks on the network.
type Host struct {
	opt Options

	mu     sync.Mutex
	srv    *Server
	port   int
	on     bool
	err    string
	link   string
	first  string // the address shown as "Listening on"
	linkAt time.Time
}

// linkMaxAge is how often the address list is re-read. Addresses change when
// the PC joins another network, but listing them on every 250 ms tick is waste.
const linkMaxAge = 5 * time.Second

// NewHost makes a Host that is off. Nothing touches the disk until Enable.
func NewHost(o Options) *Host { return &Host{opt: o} }

// Enable starts the server on port. Failures are kept in Status.Err rather
// than returned, because the settings page shows them as a line of text.
func (h *Host) Enable(port int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopLocked()
	h.on, h.port, h.err = true, port, ""
	if h.srv == nil {
		srv, err := New(h.opt)
		if err != nil {
			h.err = err.Error()
			return
		}
		h.srv = srv
	}
	if err := h.srv.Start(net.JoinHostPort("", strconv.Itoa(port))); err != nil {
		h.err = err.Error()
		return
	}
	h.linkAt = time.Time{}
}

// Disable stops the server. The certificate and token stay, so phones that
// were paired still work when it is turned on again.
func (h *Host) Disable() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopLocked()
	h.on, h.err, h.link = false, "", ""
}

func (h *Host) stopLocked() {
	if h.srv != nil {
		h.srv.Stop()
	}
}

// Forget makes a new token, so every paired phone must pair again.
func (h *Host) Forget() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.srv == nil {
		srv, err := New(h.opt)
		if err != nil {
			return err
		}
		h.srv = srv
	}
	h.linkAt = time.Time{}
	return h.srv.ResetToken()
}

// Status reports the current state.
func (h *Host) Status() Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := Status{On: h.on, Port: h.port, Err: h.err}
	if !h.on || h.err != "" || h.srv == nil {
		return st
	}
	if h.linkAt.IsZero() || time.Since(h.linkAt) > linkMaxAge {
		hosts := Hosts(h.port)
		h.link = PairingLink(h.opt.Name, hosts, h.srv.Token(), h.srv.Fingerprint())
		h.linkAt = time.Now()
		h.first = firstOr(hosts, net.JoinHostPort("localhost", strconv.Itoa(h.port)))
	}
	st.Link, st.Listening = h.link, h.first
	return st
}

func firstOr(hosts []string, def string) string {
	if len(hosts) > 0 {
		return hosts[0]
	}
	return def
}
