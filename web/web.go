// Package web serves the page that controls the light and the API it calls.
// The same handler serves the Docker image over HTTP and the native app's
// window through MyGo's mygo:// scheme, so both show the same page.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/oneryx/yeelight/light"
)

//go:embed static
var embedded embed.FS

// Static returns the page's files embedded into the program.
func Static() fs.FS {
	sub, _ := fs.Sub(embedded, "static")
	return sub
}

// waitTimeout is how long a request for the state waits for it to change.
const waitTimeout = 25 * time.Second

// Options configure a Server.
type Options struct {
	// AddressFile keeps the light's address, entered by the user or else
	// discovered. Without one the address is kept in memory only.
	AddressFile string
	// DefaultAddress is the light's "ip[:port]" until one is saved, or ""
	// to discover it.
	DefaultAddress string
	// SchemesFile keeps the schemes saved on the page. Without one they're
	// kept in memory only.
	SchemesFile string
	// Static serves the page's files; Static() when nil.
	Static fs.FS
}

// Server controls the light for the page.
type Server struct {
	ctl         *light.Controller
	addressFile string
	schemesFile string
	static      fs.FS

	mu      sync.Mutex
	state   State
	changed chan struct{} // closed when state changes
}

// State is what the page shows.
type State struct {
	// Version changes with every change of the state.
	Version   int         `json:"version"`
	Loading   bool        `json:"loading"`   // waiting for the light's first answer
	Connected bool        `json:"connected"` // the last exchange with the light succeeded
	Error     string      `json:"error"`     // why the last exchange failed
	Props     light.Props `json:"props"`     // the light's state as last read
	Address   string      `json:"address"`   // the light's address set, "" to discover it
	Schemes   []Scheme    `json:"schemes"`   // the saved schemes, in the order saved
}

// Scheme is a setting of the light saved under a name, to apply it again.
type Scheme struct {
	Name   string `json:"name"`
	Mode   string `json:"mode"`   // "white" for CT, "color" for Hue and Sat
	Bright int    `json:"bright"` // 1-100
	CT     int    `json:"ct"`     // 1700-6500 K
	Hue    int    `json:"hue"`    // 0-359
	Sat    int    `json:"sat"`    // 0-100
}

// maxNameLen is the most characters of a scheme's name.
const maxNameLen = 40

// cleanName returns the name trimmed, or an error if it can't name a scheme.
func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("the name is empty")
	}
	if utf8.RuneCountInString(name) > maxNameLen {
		return "", fmt.Errorf("the name is longer than %d characters", maxNameLen)
	}
	return name, nil
}

func (sc Scheme) validate() error {
	switch {
	case sc.Mode != "white" && sc.Mode != "color":
		return errors.New(`mode must be "white" or "color"`)
	case sc.Bright < 1 || sc.Bright > 100:
		return errors.New("brightness must be 1-100")
	case sc.CT < 1700 || sc.CT > 6500:
		return errors.New("color temperature must be 1700-6500 K")
	case sc.Hue < 0 || sc.Hue > 359 || sc.Sat < 0 || sc.Sat > 100:
		return errors.New("hue must be 0-359 and saturation 0-100")
	}
	return nil
}

// set returns the command that sets the light to the scheme, turning it on.
func (sc Scheme) set(y *light.Yeelight) error {
	if sc.Mode == "white" {
		return y.SetSceneCT(sc.CT, sc.Bright)
	}
	return y.SetSceneHSV(sc.Hue, sc.Sat, sc.Bright)
}

// New returns a server of the light at the address saved in
// opts.AddressFile, else at opts.DefaultAddress, else of the light it
// discovers, with the schemes saved in opts.SchemesFile. Run starts
// controlling it.
func New(opts Options) *Server {
	address := opts.DefaultAddress
	if opts.AddressFile != "" {
		if b, err := os.ReadFile(opts.AddressFile); err == nil {
			address = strings.TrimSpace(string(b))
		}
	}
	schemes := []Scheme{}
	if opts.SchemesFile != "" {
		if b, err := os.ReadFile(opts.SchemesFile); err == nil {
			if err := json.Unmarshal(b, &schemes); err != nil {
				log.Println("read schemes:", err)
				schemes = []Scheme{}
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			log.Println("read schemes:", err)
		}
	}
	s := &Server{
		addressFile: opts.AddressFile,
		schemesFile: opts.SchemesFile,
		static:      opts.Static,
		state:       State{Version: 1, Loading: true, Address: address, Schemes: schemes},
		changed:     make(chan struct{}),
	}
	if s.static == nil {
		s.static = Static()
	}
	s.ctl = light.NewController(address, s.report)
	s.ctl.OnDiscover(s.discovered)
	return s
}

// Run controls the light until ctx is done.
func (s *Server) Run(ctx context.Context) { s.ctl.Run(ctx) }

// report takes the light's state, or the error reading it.
func (s *Server) report(p light.Props, err error) {
	s.update(func(st *State) {
		st.Loading = false
		if err != nil {
			st.Connected, st.Error = false, err.Error()
			return
		}
		st.Connected, st.Error, st.Props = true, "", p
	})
}

// discovered keeps the address of the light discovered, as if entered,
// unless one was entered meanwhile.
func (s *Server) discovered(address string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Address != "" {
		return
	}
	s.state.Address = address
	s.notify()
	if err := s.saveAddress(address); err != nil {
		log.Println("save address:", err)
	}
}

// saveAddress saves the light's address, with s.mu held.
func (s *Server) saveAddress(address string) error {
	if s.addressFile == "" {
		return nil
	}
	return os.WriteFile(s.addressFile, []byte(address), 0o644)
}

// update changes the state with fn and wakes the requests waiting for it.
func (s *Server) update(fn func(*State)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.state)
	s.notify()
}

// notify marks the state changed, with s.mu held.
func (s *Server) notify() {
	s.state.Version++
	close(s.changed)
	s.changed = make(chan struct{})
}

// State returns the current state.
func (s *Server) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Handler serves the page at / and its API at /api/.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(s.static))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /api/state", s.getState)
	mux.HandleFunc("POST /api/refresh", s.refresh)
	mux.HandleFunc("PUT /api/address", s.setAddress)
	mux.HandleFunc("POST /api/power", command(s, func(v struct{ On bool }) (string, func(*light.Yeelight) error, error) {
		return "power", func(y *light.Yeelight) error { return y.SetPower(v.On) }, nil
	}))
	mux.HandleFunc("POST /api/bright", command(s, func(v struct{ Value int }) (string, func(*light.Yeelight) error, error) {
		if v.Value < 1 || v.Value > 100 {
			return "", nil, errors.New("brightness must be 1-100")
		}
		return "bright", func(y *light.Yeelight) error { return y.SetBright(v.Value) }, nil
	}))
	// Temperature and color share a kind: the last one sent wins.
	mux.HandleFunc("POST /api/ct", command(s, func(v struct{ Value int }) (string, func(*light.Yeelight) error, error) {
		if v.Value < 1700 || v.Value > 6500 {
			return "", nil, errors.New("color temperature must be 1700-6500 K")
		}
		return "color", func(y *light.Yeelight) error { return y.SetCT(v.Value) }, nil
	}))
	mux.HandleFunc("POST /api/hsv", command(s, func(v struct{ Hue, Sat int }) (string, func(*light.Yeelight) error, error) {
		if v.Hue < 0 || v.Hue > 359 || v.Sat < 0 || v.Sat > 100 {
			return "", nil, errors.New("hue must be 0-359 and saturation 0-100")
		}
		return "color", func(y *light.Yeelight) error { return y.SetHSV(v.Hue, v.Sat) }, nil
	}))
	mux.HandleFunc("POST /api/schemes", s.saveScheme)
	mux.HandleFunc("PUT /api/schemes/{name}", s.renameScheme)
	mux.HandleFunc("DELETE /api/schemes/{name}", s.deleteScheme)
	mux.HandleFunc("POST /api/schemes/{name}/apply", s.applyScheme)
	return mux
}

// command handles a request whose JSON body parse turns into a command for
// the light. The command is queued, and its outcome shows in the state.
func command[T any](s *Server, parse func(T) (string, func(*light.Yeelight) error, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var v T
		if err := decode(w, r, &v); err != nil {
			return
		}
		kind, fn, err := parse(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		s.ctl.Send(kind, fn)
		w.WriteHeader(http.StatusAccepted)
	}
}

// getState returns the state. With ?wait=<version>, it waits up to
// waitTimeout for a state of another version first.
func (s *Server) getState(w http.ResponseWriter, r *http.Request) {
	if wait := r.URL.Query().Get("wait"); wait != "" {
		s.mu.Lock()
		same, changed := wait == strconv.Itoa(s.state.Version), s.changed
		s.mu.Unlock()
		if same {
			select {
			case <-changed:
			case <-time.After(waitTimeout):
			case <-r.Context().Done():
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, s.State())
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	s.update(func(st *State) { st.Loading = true })
	s.ctl.Refresh()
	w.WriteHeader(http.StatusAccepted)
}

// setAddress changes the light controlled, and saves its address. "" discovers
// the light again, and keeps the address found.
func (s *Server) setAddress(w http.ResponseWriter, r *http.Request) {
	var v struct{ Address string }
	if err := decode(w, r, &v); err != nil {
		return
	}
	address := strings.TrimSpace(v.Address)
	s.mu.Lock()
	s.state = State{Version: s.state.Version, Loading: true, Address: address, Schemes: s.state.Schemes}
	s.notify()
	err := s.saveAddress(address)
	s.mu.Unlock()
	s.ctl.SetHost(address)
	if err != nil {
		log.Println("save address:", err)
		writeError(w, http.StatusInternalServerError, errors.New("the address is used but couldn't be saved: "+err.Error()))
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// saveScheme saves a scheme, replacing the one of the same name.
func (s *Server) saveScheme(w http.ResponseWriter, r *http.Request) {
	var sc Scheme
	if err := decode(w, r, &sc); err != nil {
		return
	}
	var err error
	if sc.Name, err = cleanName(sc.Name); err == nil {
		err = sc.validate()
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.editSchemes(w, func(schemes []Scheme) ([]Scheme, int, error) {
		if i := slices.IndexFunc(schemes, func(o Scheme) bool { return o.Name == sc.Name }); i >= 0 {
			schemes[i] = sc
			return schemes, 0, nil
		}
		return append(schemes, sc), 0, nil
	})
}

// renameScheme renames the scheme {name} to the body's name.
func (s *Server) renameScheme(w http.ResponseWriter, r *http.Request) {
	var v struct{ Name string }
	if err := decode(w, r, &v); err != nil {
		return
	}
	name, err := cleanName(v.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	old := r.PathValue("name")
	s.editSchemes(w, func(schemes []Scheme) ([]Scheme, int, error) {
		i := slices.IndexFunc(schemes, func(o Scheme) bool { return o.Name == old })
		if i < 0 {
			return nil, http.StatusNotFound, fmt.Errorf("no scheme %q", old)
		}
		if name != old && slices.ContainsFunc(schemes, func(o Scheme) bool { return o.Name == name }) {
			return nil, http.StatusConflict, fmt.Errorf("a scheme %q already exists", name)
		}
		schemes[i].Name = name
		return schemes, 0, nil
	})
}

func (s *Server) deleteScheme(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.editSchemes(w, func(schemes []Scheme) ([]Scheme, int, error) {
		i := slices.IndexFunc(schemes, func(o Scheme) bool { return o.Name == name })
		if i < 0 {
			return nil, http.StatusNotFound, fmt.Errorf("no scheme %q", name)
		}
		return slices.Delete(schemes, i, i+1), 0, nil
	})
}

// applyScheme sets the light to the scheme {name}, overriding the power,
// brightness and color not sent yet.
func (s *Server) applyScheme(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.mu.Lock()
	i := slices.IndexFunc(s.state.Schemes, func(o Scheme) bool { return o.Name == name })
	var sc Scheme
	if i >= 0 {
		sc = s.state.Schemes[i]
	}
	s.mu.Unlock()
	if i < 0 {
		writeError(w, http.StatusNotFound, fmt.Errorf("no scheme %q", name))
		return
	}
	s.ctl.Send("scheme", sc.set, "power", "bright", "color")
	w.WriteHeader(http.StatusAccepted)
}

// editSchemes changes the schemes with fn, which is given a copy of them and
// returns them changed, or the status and error to answer instead. The
// schemes are saved before the change shows in the state.
func (s *Server) editSchemes(w http.ResponseWriter, fn func([]Scheme) ([]Scheme, int, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	schemes, code, err := fn(slices.Clone(s.state.Schemes))
	if err != nil {
		writeError(w, code, err)
		return
	}
	if s.schemesFile != "" {
		b, _ := json.MarshalIndent(schemes, "", "  ")
		if err := os.WriteFile(s.schemesFile, b, 0o644); err != nil {
			log.Println("save schemes:", err)
			writeError(w, http.StatusInternalServerError, errors.New("couldn't save the schemes: "+err.Error()))
			return
		}
	}
	s.state.Schemes = schemes
	s.notify()
	w.WriteHeader(http.StatusNoContent)
}

// decode reads the request's JSON body into v, or answers 400.
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(v)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
	}
	return err
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
