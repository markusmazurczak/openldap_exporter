package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	baseDN    = "cn=Monitor"
	opsBaseDN = "cn=Operations,cn=Monitor"

	monitorCounterObject = "monitorCounterObject"
	monitorCounter       = "monitorCounter"

	monitoredObject = "monitoredObject"
	monitoredInfo   = "monitoredInfo"

	monitorOperation   = "monitorOperation"
	monitorOpCompleted = "monitorOpCompleted"
	monitorOpInitiated = "monitorOpInitiated"

	monitorReplicationFilter = "contextCSN"
	monitorReplication       = "monitorReplication"

	// Neither connecting nor a single LDAP operation may block a scrape.
	ldapTimeout = 10 * time.Second
)

type query struct {
	baseDN       string
	searchFilter string
	searchAttr   string
	metric       *prometheus.GaugeVec
	setData      func([]*ldap.Entry, *query)
}

// Metric names and labels are unchanged from the original (dashboards, alerts depend on them).
var (
	monitoredObjectGauge      = newGauge("monitored_object", help(baseDN, objectClass(monitoredObject), monitoredInfo), "dn")
	monitorCounterObjectGauge = newGauge("monitor_counter_object",
		help(baseDN, objectClass(monitorCounterObject), monitorCounter), "dn")
	monitorOperationGauge = newGauge("monitor_operation",
		help(opsBaseDN, objectClass(monitorOperation), monitorOpCompleted), "dn")
	monitorOperationInitiatedGauge = newGauge("monitor_operation_initiated",
		help(opsBaseDN, objectClass(monitorOperation), monitorOpInitiated), "dn")
	monitorReplicationGauge = newGauge("monitor_replication", help(baseDN, monitorReplication), "id", "type")

	bindCounter   = newCounter("bind", "successful vs unsuccessful ldap bind attempts")
	dialCounter   = newCounter("dial", "successful vs unsuccessful ldap dial attempts")
	scrapeCounter = newCounter("scrape", "successful vs unsuccessful ldap scrape attempts")
)

func init() {
	prometheus.MustRegister(
		monitoredObjectGauge,
		monitorCounterObjectGauge,
		monitorOperationGauge,
		monitorOperationInitiatedGauge,
		monitorReplicationGauge,
		scrapeCounter,
		bindCounter,
		dialCounter,
	)
}

func newGauge(name, helpText string, labels ...string) *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(prometheus.GaugeOpts{Subsystem: "openldap", Name: name, Help: helpText}, labels)
}

func newCounter(name, helpText string) *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{Subsystem: "openldap", Name: name, Help: helpText}, []string{"result"})
}

func help(msg ...string) string { return strings.Join(msg, " ") }

func objectClass(name string) string { return fmt.Sprintf("(objectClass=%v)", name) }

// baseQueries returns a new slice per call so each scraper can append its own
// replication queries.
func baseQueries() []*query {
	return []*query{
		{baseDN, objectClass(monitoredObject), monitoredInfo, monitoredObjectGauge, setValue},
		{baseDN, objectClass(monitorCounterObject), monitorCounter, monitorCounterObjectGauge, setValue},
		{opsBaseDN, objectClass(monitorOperation), monitorOpCompleted, monitorOperationGauge, setValue},
		{opsBaseDN, objectClass(monitorOperation), monitorOpInitiated, monitorOperationInitiatedGauge, setValue},
	}
}

func setValue(entries []*ldap.Entry, q *query) {
	for _, entry := range entries {
		val := entry.GetAttributeValue(q.searchAttr)
		if val == "" {
			continue // not every entry has this attribute
		}
		num, err := strconv.ParseFloat(val, 64)
		if err != nil {
			continue // some of these attributes are not numbers
		}
		q.metric.WithLabelValues(entry.DN).Set(num)
	}
}

type Scraper struct {
	Net     string
	Addr    string
	User    string
	Pass    string
	Tick    time.Duration
	Sync    []string
	log     *slog.Logger
	queries []*query
}

func (s *Scraper) Start(ctx context.Context) {
	s.log = slog.With("component", "scraper")
	s.queries = append(baseQueries(), s.replicationQueries()...)
	s.log.Info("starting monitor loop", "addr", s.Addr)
	s.checkMonitorBackend()
	ticker := time.NewTicker(s.Tick)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.scrape(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (s *Scraper) replicationQueries() []*query {
	var qs []*query
	for _, dn := range s.Sync {
		qs = append(qs, &query{dn, objectClass("*"), monitorReplicationFilter, monitorReplicationGauge, s.setReplicationValue})
	}
	return qs
}

// checkMonitorBackend checks once at startup that cn=Monitor is readable and explains
// clearly why all later scrapes would fail otherwise.
func (s *Scraper) checkMonitorBackend() {
	conn, err := dialLDAP(s.Net, s.Addr)
	if err != nil {
		s.log.Warn("startup check: dial failed", "err", err)
		return
	}
	defer closeConn(conn, s.log)
	if s.User != "" && s.Pass != "" {
		if err = conn.Bind(s.User, s.Pass); err != nil {
			s.log.Warn("startup check: bind failed — monitor queries will fail", "err", err)
			return
		}
	}
	req := ldap.NewSearchRequest(baseDN, ldap.ScopeBaseObject, ldap.NeverDerefAliases, 0, 0, false,
		"(objectClass=*)", []string{"cn"}, nil)
	if _, err = conn.Search(req); err != nil {
		if ldap.IsErrorWithCode(err, ldap.LDAPResultNoSuchObject) {
			s.log.Error("startup check: cn=Monitor not found — enable the OpenLDAP monitor backend and ensure the bind user has read access")
		} else {
			s.log.Warn("startup check: cn=Monitor search failed", "err", err)
		}
		return
	}
	s.log.Info("startup check: cn=Monitor is accessible")
}

// setReplicationValue parses contextCSN (time#count#sid#mod). Incomplete values are
// skipped instead of crashing the exporter.
func (s *Scraper) setReplicationValue(entries []*ldap.Entry, q *query) {
	for _, entry := range entries {
		val := entry.GetAttributeValue(q.searchAttr)
		if val == "" {
			continue
		}
		ll := s.log.With("filter", q.searchFilter, "attr", q.searchAttr, "value", val)
		parts := strings.Split(val, "#")
		if len(parts) < 4 {
			ll.Warn("unexpected contextCSN format")
			continue
		}
		gt, err := time.Parse("20060102150405.999999Z", parts[0])
		if err != nil {
			ll.Warn("unexpected gt value", "err", err)
			continue
		}
		count, err := strconv.ParseFloat(parts[1], 64)
		if err != nil {
			ll.Warn("unexpected count value", "err", err)
			continue
		}
		mod, err := strconv.ParseFloat(parts[3], 64)
		if err != nil {
			ll.Warn("unexpected mod value", "err", err)
			continue
		}
		sid := parts[2]
		q.metric.WithLabelValues(sid, "gt").Set(float64(gt.Unix()))
		q.metric.WithLabelValues(sid, "count").Set(count)
		q.metric.WithLabelValues(sid, "mod").Set(mod)
	}
}

// scrape runs all queries. On shutdown (ctx) it stops between queries so a stalled LDAP
// server cannot delay the stop by queries x 10 s.
func (s *Scraper) scrape(ctx context.Context) {
	conn, err := dialLDAP(s.Net, s.Addr)
	if err != nil {
		s.log.Error("dial failed", "err", err)
		dialCounter.WithLabelValues("fail").Inc()
		return
	}
	dialCounter.WithLabelValues("ok").Inc()
	defer closeConn(conn, s.log)

	if s.User != "" && s.Pass != "" {
		if err = conn.Bind(s.User, s.Pass); err != nil {
			s.log.Error("bind failed", "err", err)
			bindCounter.WithLabelValues("fail").Inc()
			return
		}
		bindCounter.WithLabelValues("ok").Inc()
	}

	result := "ok"
	for _, q := range s.queries {
		if ctx.Err() != nil {
			return // shutting down: do not count a result, the process ends
		}
		if err = scrapeQuery(conn, q); err != nil {
			s.log.Warn("query failed", "baseDN", q.baseDN, "filter", q.searchFilter, "err", err)
			if ldap.IsErrorWithCode(err, ldap.LDAPResultNoSuchObject) {
				s.log.Warn("base DN not found — check that the OpenLDAP monitor backend is enabled and the bind user has read access", "baseDN", q.baseDN)
			}
			result = "fail"
		}
	}
	scrapeCounter.WithLabelValues(result).Inc()
}

// closeConn closes an LDAP connection; a failed close is logged, not fatal (the scrape
// result is already counted).
func closeConn(conn *ldap.Conn, log *slog.Logger) {
	if err := conn.Close(); err != nil {
		log.Debug("closing LDAP connection failed", "err", err)
	}
}

func scrapeQuery(conn *ldap.Conn, q *query) error {
	req := ldap.NewSearchRequest(q.baseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		q.searchFilter, []string{q.searchAttr}, nil)
	sr, err := conn.Search(req)
	if err != nil {
		return err
	}
	q.setData(sr.Entries, q)
	return nil
}

// ldapURL turns network and address into a URL for ldap.DialURL. Addresses with a scheme
// (ldap://, ldaps://, ldapi://) are kept as is; otherwise the network decides.
// A scheme only counts with "://": url.Parse would read "ldap:10389" (host "ldap") as
// scheme "ldap".
func ldapURL(network, addr string) (string, error) {
	if strings.Contains(addr, "://") {
		u, err := url.Parse(addr)
		if err != nil {
			return "", fmt.Errorf("invalid LDAP address %q: %w", addr, err)
		}
		switch u.Scheme {
		case "ldap", "ldaps", "ldapi":
			return addr, nil
		}
		return "", fmt.Errorf("unsupported LDAP scheme %q", u.Scheme)
	}
	switch network {
	case "tcp", "tcp4", "tcp6":
		return "ldap://" + addr, nil
	case "unix":
		return "ldapi://" + url.PathEscape(addr), nil
	}
	return "", fmt.Errorf("unsupported LDAP network %q", network)
}

func dialLDAP(network, addr string) (*ldap.Conn, error) {
	u, err := ldapURL(network, addr)
	if err != nil {
		return nil, err
	}
	opts := []ldap.DialOpt{ldap.DialWithDialer(&net.Dialer{Timeout: ldapTimeout})}
	if parsed, perr := url.Parse(u); perr == nil && parsed.Scheme == "ldaps" {
		opts = append(opts, ldap.DialWithTLSConfig(&tls.Config{
			ServerName: parsed.Hostname(),
			MinVersion: tls.VersionTLS12,
		}))
	}
	conn, err := ldap.DialURL(u, opts...)
	if err != nil {
		return nil, err
	}
	conn.SetTimeout(ldapTimeout)
	return conn, nil
}
