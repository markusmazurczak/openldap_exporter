package main

import (
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-ldap/ldap/v3"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestLDAPURL(t *testing.T) {
	cases := []struct {
		network, addr, want string
		wantErr             bool
	}{
		{"tcp", "ldap:10389", "ldap://ldap:10389", false},
		{"tcp", "localhost:389", "ldap://localhost:389", false},
		{"tcp", "ldap://ldap:10389", "ldap://ldap:10389", false},
		{"tcp", "ldaps://ldap.example.org:636", "ldaps://ldap.example.org:636", false},
		{"unix", "/run/slapd/ldapi", "ldapi://%2Frun%2Fslapd%2Fldapi", false},
		{"udp", "ldap:389", "", true},
	}
	for _, c := range cases {
		got, err := ldapURL(c.network, c.addr)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("ldapURL(%q, %q) = %q, %v; want %q, err=%v", c.network, c.addr, got, err, c.want, c.wantErr)
		}
	}
}

func TestParseReplicationObjects(t *testing.T) {
	got := parseReplicationObjects([]string{"dc=a,dc=b"}, " dc=c,dc=d | |dc=e ")
	want := []string{"dc=a,dc=b", "dc=c,dc=d", "dc=e"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := parseReplicationObjects(nil, ""); len(got) != 0 {
		t.Errorf("empty input: got %v", got)
	}
}

func entry(dn, attr, val string) *ldap.Entry {
	return ldap.NewEntry(dn, map[string][]string{attr: {val}})
}

func TestSetValueIgnoresNonNumbers(t *testing.T) {
	g := newGauge("test_set_value", "test", "dn")
	q := &query{searchAttr: monitorCounter, metric: g}
	setValue([]*ldap.Entry{
		entry("cn=Total,cn=Connections,cn=Monitor", monitorCounter, "42"),
		entry("cn=Text,cn=Monitor", monitorCounter, "not a number"),
		entry("cn=Empty,cn=Monitor", "anderes", "1"),
	}, q)
	if v := testutil.ToFloat64(g.WithLabelValues("cn=Total,cn=Connections,cn=Monitor")); v != 42 {
		t.Errorf("value = %v, want 42", v)
	}
	if n := testutil.CollectAndCount(g); n != 1 {
		t.Errorf("%d series, want 1", n)
	}
}

func TestSetReplicationValue(t *testing.T) {
	g := newGauge("test_replication", "test", "id", "type")
	s := &Scraper{log: slog.Default()}
	q := &query{searchAttr: monitorReplicationFilter, metric: g}
	s.setReplicationValue([]*ldap.Entry{
		entry("dc=ok", monitorReplicationFilter, "20261125120000.123456Z#000005#001#000000"),
		entry("dc=kurz", monitorReplicationFilter, "20261125120000.123456Z#000005"), // crashed the previous version
		entry("dc=kaputt", monitorReplicationFilter, "kein-datum#1#002#0"),
	}, q)
	want := float64(time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC).Unix())
	if v := testutil.ToFloat64(g.WithLabelValues("001", "gt")); v != want {
		t.Errorf("gt = %v, want %v", v, want)
	}
	if v := testutil.ToFloat64(g.WithLabelValues("001", "count")); v != 5 {
		t.Errorf("count = %v, want 5", v)
	}
	if n := testutil.CollectAndCount(g); n != 3 {
		t.Errorf("%d series, want 3 (only dc=ok)", n)
	}
}

func TestBaseQueriesAreIndependent(t *testing.T) {
	a, b := baseQueries(), baseQueries()
	a = append(a, &query{baseDN: "dc=x"})
	if len(b) != 4 || len(a) != 5 {
		t.Errorf("slices share memory: len(a)=%d len(b)=%d", len(a), len(b))
	}
}

func TestHealthcheck(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ok.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(ok.URL, "http://"))

	if err := healthcheck(":"+port, "/metrics", time.Second); err != nil {
		t.Errorf("healthy endpoint: %v", err)
	}
	if err := healthcheck(":"+port, "/falsch", time.Second); err == nil {
		t.Error("404 must be an error")
	}
	if err := healthcheck("kein-port", "/metrics", time.Second); err == nil {
		t.Error("invalid address must be an error")
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	_, closedPort, _ := net.SplitHostPort(l.Addr().String())
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := healthcheck(":"+closedPort, "/metrics", time.Second); err == nil {
		t.Error("closed port must be an error")
	}
}

func TestServerHasTimeouts(t *testing.T) {
	s := NewMetricsServer(":0", "/metrics")
	if s.server.ReadHeaderTimeout == 0 || s.server.ReadTimeout == 0 || s.server.WriteTimeout == 0 {
		t.Error("HTTP server without timeouts")
	}
}
