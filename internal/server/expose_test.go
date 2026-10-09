package server

import (
	"encoding/binary"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// Two labelled services plus a headless one, as the API server returns them.
const serviceList = `{"items":[
 {"spec":{"clusterIP":"10.0.238.163","clusterIPs":["10.0.238.163"],"ports":[{"port":80,"protocol":"TCP"}]}},
 {"spec":{"clusterIP":"10.0.50.5","ports":[{"port":53,"protocol":"UDP"},{"port":8080}]}},
 {"spec":{"clusterIP":"None","ports":[{"port":5432,"protocol":"TCP"}]}}
]}`

func TestParseServices(t *testing.T) {
	got, err := parseServices([]byte(serviceList))
	if err != nil {
		t.Fatal(err)
	}
	want := []svcPort{
		{IP: "10.0.238.163", Proto: "tcp", Port: 80},
		{IP: "10.0.50.5", Proto: "udp", Port: 53},
		{IP: "10.0.50.5", Proto: "tcp", Port: 8080}, // protocol defaults to TCP
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseServices = %+v, want %+v (headless services are skipped)", got, want)
	}
}

// ipv4 builds a minimal IPv4 header (+ 4 bytes of L4 ports) for proto to dst:port.
func ipv4(proto byte, dst string, port uint16, fragOffset uint16) []byte {
	p := make([]byte, 24)
	p[0] = 0x45 // v4, IHL 5
	binary.BigEndian.PutUint16(p[6:8], fragOffset)
	p[9] = proto
	copy(p[16:20], net.ParseIP(dst).To4())
	binary.BigEndian.PutUint16(p[22:24], port)
	return p
}

func TestAllowPacket(t *testing.T) {
	e := newExposeSet("10.0.0.10")
	e.set([]svcPort{{IP: "10.0.238.163", Proto: "tcp", Port: 80}})
	e.allowSelf("10.8.0.1", 9100) // the auth-vpn dashboard/API, reached over the tunnel

	tests := []struct {
		name string
		pkt  []byte
		want bool
	}{
		{"exposed service and port", ipv4(6, "10.0.238.163", 80, 0), true},
		{"exposed service, other port", ipv4(6, "10.0.238.163", 3000, 0), false},
		{"unlabelled service (redis)", ipv4(6, "10.0.29.209", 6379, 0), false},
		{"cluster DNS over UDP", ipv4(17, "10.0.0.10", 53, 0), true},
		{"cluster DNS over TCP", ipv4(6, "10.0.0.10", 53, 0), true},
		{"cluster DNS, other port", ipv4(17, "10.0.0.10", 9153, 0), false},
		{"ICMP", ipv4(1, "10.0.238.163", 0, 0), false},
		{"auth-vpn dashboard/API on the server's tunnel IP", ipv4(6, "10.8.0.1", 9100, 0), true},
		{"server's tunnel IP, other port (SSH)", ipv4(6, "10.8.0.1", 2222, 0), false},
		{"server's tunnel IP over UDP", ipv4(17, "10.8.0.1", 9100, 0), false},
		{"non-first fragment (no ports to check)", ipv4(6, "10.0.238.163", 80, 185), false},
		{"truncated packet", ipv4(6, "10.0.238.163", 80, 0)[:21], false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := e.allowPacket(tt.pkt); got != tt.want {
				t.Errorf("allowPacket = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolveTarget(t *testing.T) {
	e := newExposeSet("10.0.0.10")
	e.set([]svcPort{{IP: "10.0.238.163", Proto: "tcp", Port: 80}})
	lookup := func(host string) ([]net.IP, error) {
		switch host {
		case "grafana-lb.default.svc.cluster.local":
			return []net.IP{net.ParseIP("10.0.238.163")}, nil
		case "redis.default.svc.cluster.local":
			return []net.IP{net.ParseIP("10.0.29.209")}, nil
		}
		return nil, errors.New("no such host")
	}

	tests := []struct {
		host    string
		port    uint16
		want    string
		wantErr bool
	}{
		{"grafana-lb.default.svc.cluster.local", 80, "10.0.238.163", false},
		{"10.0.238.163", 80, "10.0.238.163", false},
		{"grafana-lb.default.svc.cluster.local", 3000, "", true},
		{"redis.default.svc.cluster.local", 6379, "", true},
		{"169.254.169.254", 80, "", true}, // cloud metadata: never reachable in labeled mode
		{"nope.example", 80, "", true},
	}
	for _, tt := range tests {
		got, err := e.resolveTarget(tt.host, tt.port, lookup)
		if got != tt.want || (err != nil) != tt.wantErr {
			t.Errorf("resolveTarget(%s, %d) = %q, %v; want %q, err=%v", tt.host, tt.port, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestRoutesAreExposedClusterIPs(t *testing.T) {
	e := newExposeSet("10.0.0.10")
	e.set([]svcPort{{IP: "10.0.50.5", Proto: "udp", Port: 53}, {IP: "10.0.50.5", Proto: "tcp", Port: 8080}, {IP: "10.0.238.163", Proto: "tcp", Port: 80}})
	if got, want := e.routes(), []string{"10.0.238.163/32", "10.0.50.5/32"}; !reflect.DeepEqual(got, want) {
		t.Errorf("routes = %v, want %v", got, want)
	}
}

func TestFetchExposed(t *testing.T) {
	var gotAuth, gotSelector string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotSelector = r.Header.Get("Authorization"), r.URL.Query().Get("labelSelector")
		if r.URL.Path != "/api/v1/services" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(serviceList)) //nolint:errcheck
	}))
	defer api.Close()

	got, err := fetchExposed(api.Client(), api.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || gotAuth != "Bearer tok" || gotSelector != "auth-vpn.io/expose=true" {
		t.Errorf("fetchExposed: %d ports, auth=%q, selector=%q", len(got), gotAuth, gotSelector)
	}

	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer forbidden.Close()
	if _, err := fetchExposed(forbidden.Client(), forbidden.URL, "tok"); err == nil {
		t.Error("fetchExposed: want error on 403 (missing RBAC)")
	}
}

func TestPushRoutes(t *testing.T) {
	e := newExposeSet("10.0.0.10")
	e.set([]svcPort{{IP: "10.0.238.163", Proto: "tcp", Port: 80}})
	tests := []struct {
		name string
		srv  *Server
		want []string
	}{
		{"all mode: configured routes only", &Server{cfg: &Config{PushRoutes: []string{"10.0.0.0/16"}}}, []string{"10.0.0.0/16"}},
		{"labeled mode: configured routes + exposed /32s", &Server{cfg: &Config{PushRoutes: []string{"10.30.0.0/16"}}, expose: e}, []string{"10.30.0.0/16", "10.0.238.163/32"}},
		{"no_push: nothing, even in labeled mode", &Server{cfg: &Config{NoPush: true}, expose: e}, nil},
	}
	for _, tt := range tests {
		if got := tt.srv.pushRoutes(); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: pushRoutes = %v, want %v", tt.name, got, tt.want)
		}
	}
}
