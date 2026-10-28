package light

import (
	"context"
	"slices"
	"sync"
	"time"
)

// Controller talks to the light from one goroutine, so commands reach it in
// order and the UI never waits on the network. Commands are queued by kind:
// a newer brightness replaces one not sent yet, which keeps a slider being
// dragged from flooding the light (it rejects more than about one command a
// second on average).
type Controller struct {
	// discover finds a light when no host is set; Discover by default.
	discover func() (*Yeelight, error)
	// report receives the light's state after each round of commands and
	// each refresh, or the error that stopped them. It runs on the
	// controller's goroutine.
	report func(Props, error)
	// throttle is the least time between two rounds of commands.
	throttle time.Duration
	// poll is how often the state is read while nothing is sent.
	poll time.Duration
	// discovered receives the address of a light discovered, if set.
	discovered func(host string)

	mu      sync.Mutex
	host    string    // the light's "ip[:port]", or "" to discover it once
	y       *Yeelight // the light last reached, nil to find it again
	order   []string
	pending map[string]func(*Yeelight) error
	refresh bool
	wake    chan struct{}
}

func NewController(host string, report func(Props, error)) *Controller {
	return &Controller{
		discover: Discover,
		report:   report,
		throttle: 200 * time.Millisecond,
		poll:     5 * time.Second,
		host:     host,
		pending:  map[string]func(*Yeelight) error{},
		refresh:  true,
		wake:     make(chan struct{}, 1),
	}
}

// Send queues fn, replacing a command of the same kind not sent yet, and
// dropping those of the kinds it overrides.
func (c *Controller) Send(kind string, fn func(*Yeelight) error, overrides ...string) {
	c.mu.Lock()
	for _, k := range overrides {
		if _, ok := c.pending[k]; ok {
			delete(c.pending, k)
			c.order = slices.DeleteFunc(c.order, func(o string) bool { return o == k })
		}
	}
	if _, ok := c.pending[kind]; !ok {
		c.order = append(c.order, kind)
	}
	c.pending[kind] = fn
	c.mu.Unlock()
	c.poke()
}

// OnDiscover sets fn to receive the address of a light discovered, which
// then stays the light controlled, as if set with SetHost. Call it before Run.
func (c *Controller) OnDiscover(fn func(host string)) {
	c.discovered = fn
}

// Refresh reads the light's state soon.
func (c *Controller) Refresh() {
	c.mu.Lock()
	c.refresh = true
	c.mu.Unlock()
	c.poke()
}

// SetHost changes the light controlled: "ip[:port]", or "" to discover it.
func (c *Controller) SetHost(host string) {
	c.mu.Lock()
	c.host, c.y = host, nil
	c.order, c.pending = nil, map[string]func(*Yeelight) error{}
	c.refresh = true
	c.mu.Unlock()
	c.poke()
}

func (c *Controller) poke() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Run sends queued commands and reads the light's state until ctx is done.
func (c *Controller) Run(ctx context.Context) {
	tick := time.NewTicker(c.poll)
	defer tick.Stop()
	for {
		c.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
		case <-tick.C:
			c.mu.Lock()
			c.refresh = true
			c.mu.Unlock()
		}
	}
}

// drain sends the queued commands in rounds, then reads the light's state
// once nothing is left to send.
func (c *Controller) drain(ctx context.Context) {
	sent := false
	for ctx.Err() == nil {
		c.mu.Lock()
		order, pending := c.order, c.pending
		c.order, c.pending = nil, map[string]func(*Yeelight) error{}
		refresh := c.refresh
		c.mu.Unlock()

		if len(order) == 0 {
			if refresh || sent {
				c.mu.Lock()
				c.refresh = false
				c.mu.Unlock()
				var p Props
				err := c.do(func(y *Yeelight) (err error) {
					p, err = y.Props()
					return err
				})
				if ctx.Err() == nil {
					c.report(p, err)
				}
			}
			return
		}
		for _, kind := range order {
			if err := c.do(pending[kind]); err != nil {
				c.report(Props{}, err)
				return
			}
		}
		sent = true
		select {
		case <-ctx.Done():
		case <-time.After(c.throttle):
		}
	}
}

// do runs fn against the light, discovering it first if needed.
func (c *Controller) do(fn func(*Yeelight) error) error {
	c.mu.Lock()
	host, y := c.host, c.y
	c.mu.Unlock()

	if y == nil {
		var err error
		if y, err = c.find(host); err != nil {
			return err
		}
	}
	return fn(y)
}

// find returns the light at host, or discovers one if host is "" and keeps
// its address.
func (c *Controller) find(host string) (*Yeelight, error) {
	var y *Yeelight
	if host != "" {
		y = ParseHost(host)
	} else {
		var err error
		if y, err = c.discover(); err != nil {
			return nil, err
		}
	}
	c.mu.Lock()
	same := c.host == host // not changed meanwhile
	if same {
		c.y = y
		if host == "" {
			c.host = y.Addr()
		}
	}
	c.mu.Unlock()
	if same && host == "" && c.discovered != nil {
		c.discovered(y.Addr())
	}
	return y, nil
}
