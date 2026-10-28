package light

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// The LAN protocol is described in the Yeelight Inter-Operation Spec:
// http://www.yeelight.com/download/Yeelight_Inter-Operation_Spec.pdf

const (
	timeout      = 5 * time.Second
	yeelightPort = "55443"
	// effect and duration of every change: "sudden" applies it at once,
	// which keeps up with a slider being dragged.
	effect   = "sudden"
	duration = 30 // the minimal duration, in ms
)

// Yeelight is a light on the LAN, at host:port.
type Yeelight struct {
	host, port string
}

// CommandError is an error reported by the light itself in reply to a command.
type CommandError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("yeelight error %d: %s", e.Code, e.Message)
}

type command struct {
	ID     int    `json:"id"`
	Method string `json:"method"`
	Params []any  `json:"params"`
}

type reply struct {
	ID     *int            `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *CommandError   `json:"error"`
}

var commandID atomic.Int32

// NewYeelight returns the light at host:port.
func NewYeelight(host, port string) *Yeelight {
	return &Yeelight{host: host, port: port}
}

// ParseHost returns the light at "ip" or "ip:port".
func ParseHost(s string) *Yeelight {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return NewYeelight(s, yeelightPort)
	}
	return NewYeelight(host, port)
}

// Addr returns the light's address as ParseHost takes it: "ip", or
// "ip:port" if not at the default port.
func (y *Yeelight) Addr() string {
	if y.port == yeelightPort {
		return y.host
	}
	return net.JoinHostPort(y.host, y.port)
}

// call sends a command to the light and returns the raw "result" of its reply.
// Notification messages the light may push on the same connection are skipped.
func (y *Yeelight) call(method string, params ...any) (json.RawMessage, error) {
	if params == nil {
		params = []any{}
	}
	id := int(commandID.Add(1))
	cmd, err := json.Marshal(command{ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}

	conn, err := net.DialTimeout("tcp", net.JoinHostPort(y.host, y.port), timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(append(cmd, '\r', '\n')); err != nil {
		return nil, err
	}

	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		var rep reply
		if err := json.Unmarshal(line, &rep); err != nil {
			return nil, fmt.Errorf("invalid reply %q: %w", line, err)
		}
		if rep.ID == nil || *rep.ID != id {
			continue // notification or reply to another command
		}
		if rep.Error != nil {
			return nil, rep.Error
		}
		return rep.Result, nil
	}
}

func (y *Yeelight) exec(method string, params ...any) error {
	_, err := y.call(method, params...)
	return err
}

// Discover finds the first light answering an SSDP search on the LAN.
func Discover() (*Yeelight, error) {
	addr, err := net.ResolveUDPAddr("udp4", "239.255.255.250:1982")
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_, err = conn.WriteToUDP([]byte(
		"M-SEARCH * HTTP/1.1\r\n"+
			"ST:wifi_bulb\r\n"+
			"MAN:\"ssdp:discover\"\r\n\r\n",
	), addr)
	if err != nil {
		return nil, err
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			break
		}
		if y := parseDiscoveryResponse(string(buf[:n])); y != nil {
			return y, nil
		}
	}
	return nil, fmt.Errorf("no Yeelight found on the network")
}

// parseDiscoveryResponse returns the light advertised in the
// "Location: yeelight://ip:port" header of a discovery response, or nil.
func parseDiscoveryResponse(msg string) *Yeelight {
	for _, line := range strings.Split(msg, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "location") {
			continue
		}
		u, err := url.Parse(strings.TrimSpace(value))
		if err != nil || u.Scheme != "yeelight" || u.Hostname() == "" {
			return nil
		}
		port := u.Port()
		if port == "" {
			port = yeelightPort
		}
		return NewYeelight(u.Hostname(), port)
	}
	return nil
}

// Color modes reported by the light.
const (
	ModeRGB = 1
	ModeCT  = 2
	ModeHSV = 3
)

// Props is the state of a light.
type Props struct {
	Name      string `json:"name"`
	On        bool   `json:"on"`
	Bright    int    `json:"bright"`     // 1-100
	ColorMode int    `json:"color_mode"` // ModeRGB, ModeCT or ModeHSV
	CT        int    `json:"ct"`         // 1700-6500 K
	RGB       int    `json:"rgb"`
	Hue       int    `json:"hue"` // 0-359
	Sat       int    `json:"sat"` // 0-100
	Host      string `json:"host"`
}

// Props reads the light's state.
func (y *Yeelight) Props() (Props, error) {
	names := []any{"name", "power", "bright", "color_mode", "ct", "rgb", "hue", "sat"}
	raw, err := y.call("get_prop", names...)
	if err != nil {
		return Props{}, err
	}
	var v []string
	if err := json.Unmarshal(raw, &v); err != nil {
		return Props{}, fmt.Errorf("invalid get_prop result %s: %w", raw, err)
	}
	if len(v) != len(names) {
		return Props{}, fmt.Errorf("get_prop returned %d values, want %d", len(v), len(names))
	}
	return Props{
		Name:      v[0],
		On:        v[1] == "on",
		Bright:    atoi(v[2]),
		ColorMode: atoi(v[3]),
		CT:        atoi(v[4]),
		RGB:       atoi(v[5]),
		Hue:       atoi(v[6]),
		Sat:       atoi(v[7]),
		Host:      y.host,
	}, nil
}

// SetPower switches the light on or off.
func (y *Yeelight) SetPower(on bool) error {
	power := "off"
	if on {
		power = "on"
	}
	return y.exec("set_power", power, "smooth", 300)
}

// SetBright sets the brightness, 1-100.
func (y *Yeelight) SetBright(bright int) error {
	return y.exec("set_bright", bright, effect, duration)
}

// SetCT sets the color temperature, 1700-6500 K.
func (y *Yeelight) SetCT(ct int) error {
	return y.exec("set_ct_abx", ct, effect, duration)
}

// SetHSV sets the color, hue 0-359 and saturation 0-100.
func (y *Yeelight) SetHSV(hue, sat int) error {
	return y.exec("set_hsv", hue, sat, effect, duration)
}

// SetSceneCT turns the light on at a color temperature, 1700-6500 K, and a
// brightness, 1-100, in one command.
func (y *Yeelight) SetSceneCT(ct, bright int) error {
	return y.exec("set_scene", "ct", ct, bright)
}

// SetSceneHSV turns the light on at a color, hue 0-359 and saturation 0-100,
// and a brightness, 1-100, in one command.
func (y *Yeelight) SetSceneHSV(hue, sat, bright int) error {
	return y.exec("set_scene", "hsv", hue, sat, bright)
}

func atoi(s string) int {
	i, _ := strconv.Atoi(s)
	return i
}
