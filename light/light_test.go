package light_test

import (
	"context"
	"net"
	"testing"
	"time"

	. "github.com/oneryx/yeelight/light"
	"github.com/oneryx/yeelight/light/lighttest"
)

type report struct {
	p   Props
	err error
}

// runController runs a controller of the light at addr, whose reports the
// returned channel receives.
func runController(t *testing.T, addr string) (*Controller, chan report) {
	reports := make(chan report, 16)
	ctl := NewController(addr, func(p Props, err error) { reports <- report{p, err} })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go ctl.Run(ctx)
	return ctl, reports
}

func next(t *testing.T, reports chan report) report {
	t.Helper()
	select {
	case r := <-reports:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("no report from the controller")
		return report{}
	}
}

func TestControllerReadsAndSends(t *testing.T) {
	f := lighttest.Start(t)
	ctl, reports := runController(t, f.Addr)

	r := next(t, reports)
	if r.err != nil || r.p.Name != "Desk" || !r.p.On || r.p.Bright != 40 || r.p.CT != 3000 || r.p.Host != "127.0.0.1" {
		t.Fatalf("first report %+v", r)
	}

	ctl.Send("bright", func(y *Yeelight) error { return y.SetBright(70) })
	ctl.Send("color", func(y *Yeelight) error { return y.SetHSV(120, 80) })
	r = next(t, reports)
	if r.err != nil || r.p.Bright != 70 || r.p.Hue != 120 || r.p.Sat != 80 || r.p.ColorMode != ModeHSV {
		t.Fatalf("report after sending %+v", r)
	}
}

func TestControllerCoalesces(t *testing.T) {
	f := lighttest.Start(t)
	reports := make(chan report, 16)
	ctl := NewController(f.Addr, func(p Props, err error) { reports <- report{p, err} })
	// Queued before Run, as while a slider is dragged between rounds.
	for v := 1; v <= 50; v++ {
		ctl.Send("bright", func(y *Yeelight) error { return y.SetBright(v) })
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ctl.Run(ctx)

	r := next(t, reports)
	if r.p.Bright != 50 {
		t.Fatalf("brightness %d, want the last value 50", r.p.Bright)
	}
	n := 0
	for _, m := range f.Sent() {
		if m == "set_bright" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("sent set_bright %d times, want once", n)
	}
}

func TestControllerUnreachable(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	_, reports := runController(t, addr)
	if r := next(t, reports); r.err == nil {
		t.Fatal("no error from a light that isn't there")
	}
}

func TestControllerSetHost(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	ctl, reports := runController(t, addr)
	if r := next(t, reports); r.err == nil {
		t.Fatal("no error from a light that isn't there")
	}
	f := lighttest.Start(t)
	ctl.SetHost(f.Addr)
	if r := next(t, reports); r.err != nil || r.p.Name != "Desk" {
		t.Fatalf("report after SetHost %+v", r)
	}
}

func TestControllerKeepsDiscoveredHost(t *testing.T) {
	f := lighttest.Start(t)
	discovered := make(chan string, 1)
	reports := make(chan report, 16)
	ctl := NewController("", func(p Props, err error) { reports <- report{p, err} })
	ctl.SetDiscover(func() (*Yeelight, error) { return ParseHost(f.Addr), nil })
	ctl.OnDiscover(func(host string) { discovered <- host })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go ctl.Run(ctx)

	if r := next(t, reports); r.err != nil || r.p.Name != "Desk" {
		t.Fatalf("report %+v", r)
	}
	if host := <-discovered; host != f.Addr {
		t.Errorf("discovered %q, want %q", host, f.Addr)
	}
	// Discovered once: later rounds use the address.
	ctl.SetDiscover(func() (*Yeelight, error) { t.Error("discovered again"); return nil, net.ErrClosed })
	ctl.Refresh()
	if r := next(t, reports); r.err != nil {
		t.Fatalf("report %+v", r)
	}
}

func TestAddr(t *testing.T) {
	for in, want := range map[string]string{
		"192.168.0.10":       "192.168.0.10",
		"192.168.0.10:55443": "192.168.0.10",
		"192.168.0.10:1234":  "192.168.0.10:1234",
	} {
		if got := ParseHost(in).Addr(); got != want {
			t.Errorf("ParseHost(%q).Addr() = %q, want %q", in, got, want)
		}
	}
}
