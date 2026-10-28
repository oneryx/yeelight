// Package lighttest provides a fake Yeelight for tests.
package lighttest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"sync"
	"testing"

	"github.com/oneryx/yeelight/light"
)

// Light answers the LAN protocol on localhost, as a light would.
type Light struct {
	// Addr is the light's "ip:port".
	Addr string

	mu      sync.Mutex
	power   string
	bright  int
	ct      int
	hue     int
	sat     int
	mode    int
	methods []string
}

// Start starts a light named "Desk", on at 40% and 3000 K, until the test ends.
func Start(t testing.TB) *Light {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &Light{power: "on", bright: 40, ct: 3000, mode: light.ModeCT, Addr: ln.Addr().String()}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *Light) serve(conn net.Conn) {
	defer conn.Close()
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return
	}
	var cmd struct {
		ID     int
		Method string
		Params []any
	}
	json.Unmarshal(line, &cmd)
	num := func(i int) int { return int(cmd.Params[i].(float64)) }

	f.mu.Lock()
	f.methods = append(f.methods, cmd.Method)
	var result any = []string{"ok"}
	switch cmd.Method {
	case "get_prop":
		result = []string{"Desk", f.power, strconv.Itoa(f.bright), strconv.Itoa(f.mode),
			strconv.Itoa(f.ct), "0", strconv.Itoa(f.hue), strconv.Itoa(f.sat)}
	case "set_power":
		f.power = cmd.Params[0].(string)
	case "set_bright":
		f.bright = num(0)
	case "set_ct_abx":
		f.ct, f.mode = num(0), light.ModeCT
	case "set_hsv":
		f.hue, f.sat, f.mode = num(0), num(1), light.ModeHSV
	case "set_scene":
		f.power = "on"
		switch cmd.Params[0] {
		case "ct":
			f.ct, f.bright, f.mode = num(1), num(2), light.ModeCT
		case "hsv":
			f.hue, f.sat, f.bright, f.mode = num(1), num(2), num(3), light.ModeHSV
		}
	}
	f.mu.Unlock()

	// A notification first, which the client must skip.
	fmt.Fprintf(conn, `{"method":"props","params":{"power":"on"}}`+"\r\n")
	b, _ := json.Marshal(map[string]any{"id": cmd.ID, "result": result})
	conn.Write(append(b, '\r', '\n'))
}

// Sent returns the methods the light received, in order.
func (f *Light) Sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.methods...)
}
