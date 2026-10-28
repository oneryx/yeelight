package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/oneryx/yeelight/light/lighttest"
)

// start serves a Server of opts, running until the test ends.
func start(t *testing.T, opts Options) (*Server, *httptest.Server) {
	s := New(opts)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go s.Run(ctx)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

// at returns options of the light at addr, saved in a temporary address file.
func at(t *testing.T, addr string) Options {
	file := filepath.Join(t.TempDir(), "address")
	if err := os.WriteFile(file, []byte(addr), 0o644); err != nil {
		t.Fatal(err)
	}
	return Options{AddressFile: file}
}

func do(t *testing.T, ts *httptest.Server, method, path, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

// waitState returns the first state after version for which ok holds.
func waitState(t *testing.T, ts *httptest.Server, version int, ok func(State) bool) State {
	t.Helper()
	for range 20 {
		res := do(t, ts, "GET", "/api/state?wait="+strconv.Itoa(version), "")
		var st State
		if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
			t.Fatal(err)
		}
		if ok(st) {
			return st
		}
		version = st.Version
	}
	t.Fatal("the state never got there")
	return State{}
}

func TestState(t *testing.T) {
	f := lighttest.Start(t)
	_, ts := start(t, at(t, f.Addr))

	st := waitState(t, ts, 0, func(st State) bool { return !st.Loading })
	if !st.Connected || st.Props.Name != "Desk" || !st.Props.On || st.Props.Bright != 40 || st.Address != f.Addr {
		t.Fatalf("state %+v", st)
	}

	if res := do(t, ts, "POST", "/api/power", `{"on":false}`); res.StatusCode != http.StatusAccepted {
		t.Fatalf("power: %s", res.Status)
	}
	waitState(t, ts, st.Version, func(st State) bool { return !st.Props.On })

	do(t, ts, "POST", "/api/hsv", `{"hue":120,"sat":80}`)
	st = waitState(t, ts, st.Version, func(st State) bool { return st.Props.Hue == 120 })
	if st.Props.Sat != 80 {
		t.Errorf("state after hsv %+v", st.Props)
	}
}

func TestInvalidCommands(t *testing.T) {
	_, ts := start(t, at(t, "127.0.0.1:1"))
	for path, body := range map[string]string{
		"/api/bright": `{"value":0}`,
		"/api/ct":     `{"value":9000}`,
		"/api/hsv":    `{"hue":360,"sat":0}`,
		"/api/power":  `not json`,
	} {
		res := do(t, ts, "POST", path, body)
		var e map[string]string
		json.NewDecoder(res.Body).Decode(&e)
		if res.StatusCode != http.StatusBadRequest || e["error"] == "" {
			t.Errorf("%s %s: %s %v", path, body, res.Status, e)
		}
	}
}

func TestUnreachable(t *testing.T) {
	_, ts := start(t, at(t, "127.0.0.1:1"))
	st := waitState(t, ts, 0, func(st State) bool { return !st.Loading })
	if st.Connected || st.Error == "" {
		t.Fatalf("state %+v", st)
	}
}

func TestAddress(t *testing.T) {
	opts := at(t, "127.0.0.1:1")
	file := opts.AddressFile
	f := lighttest.Start(t)
	s, ts := start(t, opts)

	if res := do(t, ts, "PUT", "/api/address", `{"address":" `+f.Addr+` "}`); res.StatusCode != http.StatusAccepted {
		t.Fatalf("address: %s", res.Status)
	}
	st := waitState(t, ts, 0, func(st State) bool { return st.Connected })
	if st.Address != f.Addr || st.Props.Name != "Desk" {
		t.Fatalf("state %+v", st)
	}
	if b, _ := os.ReadFile(file); string(b) != f.Addr {
		t.Errorf("saved %q", b)
	}

	// The saved address is used after a restart, over the default.
	if s = New(Options{AddressFile: file, DefaultAddress: "127.0.0.1:1"}); s.State().Address != f.Addr {
		t.Errorf("address %q after a restart", s.State().Address)
	}
	// The default is used until an address is saved.
	missing := filepath.Join(t.TempDir(), "address")
	if s = New(Options{AddressFile: missing, DefaultAddress: "127.0.0.1:1"}); s.State().Address != "127.0.0.1:1" {
		t.Errorf("address %q without a saved one", s.State().Address)
	}
}

func TestPage(t *testing.T) {
	_, ts := start(t, at(t, "127.0.0.1:1"))
	for _, path := range []string{"/", "/app.js", "/style.css", "/manifest.json", "/sw.js", "/healthz"} {
		if res := do(t, ts, "GET", path, ""); res.StatusCode != http.StatusOK {
			t.Errorf("%s: %s", path, res.Status)
		}
	}
}

func TestSchemes(t *testing.T) {
	file := filepath.Join(t.TempDir(), "schemes.json")
	f := lighttest.Start(t)
	opts := at(t, f.Addr)
	opts.SchemesFile = file
	_, ts := start(t, opts)

	status := func(method, path, body string) int {
		t.Helper()
		return do(t, ts, method, path, body).StatusCode
	}
	names := func() []string {
		t.Helper()
		var st State
		json.NewDecoder(do(t, ts, "GET", "/api/state", "").Body).Decode(&st)
		var names []string
		for _, sc := range st.Schemes {
			names = append(names, sc.Name)
		}
		return names
	}

	for _, body := range []string{
		`{"name":"  ","mode":"white","bright":50,"ct":3000}`,
		`{"name":"Bad","mode":"red","bright":50,"ct":3000}`,
		`{"name":"Bad","mode":"white","bright":0,"ct":3000}`,
		`{"name":"Bad","mode":"color","bright":50,"ct":3000,"hue":360}`,
	} {
		if code := status("POST", "/api/schemes", body); code != http.StatusBadRequest {
			t.Errorf("save %s: %d", body, code)
		}
	}

	status("POST", "/api/schemes", `{"name":" Reading ","mode":"white","bright":80,"ct":4500}`)
	status("POST", "/api/schemes", `{"name":"Red / dim","mode":"color","bright":10,"ct":3000,"hue":0,"sat":100}`)
	// The same name replaces the scheme in place.
	status("POST", "/api/schemes", `{"name":"Reading","mode":"white","bright":90,"ct":4500}`)
	if got := names(); !slices.Equal(got, []string{"Reading", "Red / dim"}) {
		t.Fatalf("schemes %q", got)
	}

	if code := status("PUT", "/api/schemes/Reading", `{"name":"Red / dim"}`); code != http.StatusConflict {
		t.Errorf("rename onto another: %d", code)
	}
	if code := status("PUT", "/api/schemes/Nope", `{"name":"X"}`); code != http.StatusNotFound {
		t.Errorf("rename missing: %d", code)
	}
	if code := status("PUT", "/api/schemes/Reading", `{"name":"Work"}`); code != http.StatusNoContent {
		t.Errorf("rename: %d", code)
	}

	st := waitState(t, ts, 0, func(st State) bool { return st.Connected })
	if code := status("POST", "/api/schemes/Red%20%2F%20dim/apply", ""); code != http.StatusAccepted {
		t.Fatalf("apply: %d", code)
	}
	st = waitState(t, ts, st.Version, func(st State) bool { return st.Props.ColorMode == 3 })
	if p := st.Props; !p.On || p.Bright != 10 || p.Hue != 0 || p.Sat != 100 {
		t.Errorf("props after apply %+v", p)
	}
	if code := status("POST", "/api/schemes/Nope/apply", ""); code != http.StatusNotFound {
		t.Errorf("apply missing: %d", code)
	}

	if code := status("DELETE", "/api/schemes/Work", ""); code != http.StatusNoContent {
		t.Errorf("delete: %d", code)
	}
	if got := names(); !slices.Equal(got, []string{"Red / dim"}) {
		t.Fatalf("schemes after delete %q", got)
	}

	// The schemes are kept across restarts.
	if s := New(Options{SchemesFile: file}); len(s.State().Schemes) != 1 || s.State().Schemes[0].Bright != 10 {
		t.Errorf("schemes %+v after a restart", s.State().Schemes)
	}
	// And across a change of the light.
	do(t, ts, "PUT", "/api/address", `{"address":"127.0.0.1:1"}`)
	if got := names(); !slices.Equal(got, []string{"Red / dim"}) {
		t.Errorf("schemes after a change of address %q", got)
	}
}

func TestDiscoveredAddress(t *testing.T) {
	file := filepath.Join(t.TempDir(), "address")
	s := New(Options{AddressFile: file})

	// A light discovered while no address is set keeps its address.
	s.discovered("192.168.0.20")
	if a := s.State().Address; a != "192.168.0.20" {
		t.Errorf("address %q after discovery", a)
	}
	if b, _ := os.ReadFile(file); string(b) != "192.168.0.20" {
		t.Errorf("saved %q", b)
	}

	// One entered meanwhile wins.
	s.mu.Lock()
	s.state.Address = "10.0.0.2"
	s.mu.Unlock()
	s.discovered("192.168.0.21")
	if a := s.State().Address; a != "10.0.0.2" {
		t.Errorf("address %q, want the entered one", a)
	}
}
