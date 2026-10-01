package gate

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

var zeroTime time.Time

func deadline() time.Time { return time.Now().Add(readTimeout) }

// Ask sends r to Commander and waits for the decision, which can take as
// long as the user does. Cancelling ctx hangs up, which cancels the
// Decider's context on the server. The AgentID in r is ignored by the server.
func Ask(ctx context.Context, addr, token string, r Request) (Decision, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", addr)
	if err != nil {
		return Decision{}, fmt.Errorf("can't reach Atlas Commander: %w", err)
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()

	if err := json.NewEncoder(c).Encode(message{Op: "gate", Token: token, Request: &r}); err != nil {
		return Decision{}, fmt.Errorf("can't send the request: %w", err)
	}
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		if ctx.Err() != nil {
			return Decision{}, ctx.Err()
		}
		return Decision{}, errors.New("Atlas Commander closed the connection without answering")
	}
	var rep reply
	if err := json.Unmarshal(line, &rep); err != nil {
		return Decision{}, errors.New("Atlas Commander sent an unreadable answer")
	}
	return Decision{Allow: rep.Allow, Reason: rep.Reason}, nil
}

// Activate asks a running Commander to show its window. It reports whether
// one answered.
func Activate(addr string) bool {
	c, err := net.DialTimeout("unix", addr, readTimeout)
	if err != nil {
		return false
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(readTimeout))
	if json.NewEncoder(c).Encode(message{Op: "activate"}) != nil {
		return false
	}
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		return false
	}
	var rep reply
	return json.Unmarshal(line, &rep) == nil && rep.OK
}
