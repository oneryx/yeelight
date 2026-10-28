package light

import "testing"

func TestParseDiscoveryResponse(t *testing.T) {
	y := parseDiscoveryResponse("HTTP/1.1 200 OK\r\nCache-Control: max-age=3600\r\nLocation: yeelight://192.168.0.20:55443\r\nid: 0x1\r\n")
	if y == nil || y.host != "192.168.0.20" || y.port != "55443" {
		t.Fatalf("got %+v", y)
	}
}

func TestParseHost(t *testing.T) {
	for in, want := range map[string]Yeelight{
		"192.168.0.20":       {host: "192.168.0.20", port: yeelightPort},
		"192.168.0.20:12345": {host: "192.168.0.20", port: "12345"},
	} {
		if y := ParseHost(in); *y != want {
			t.Errorf("ParseHost(%q) = %+v, want %+v", in, *y, want)
		}
	}
}
