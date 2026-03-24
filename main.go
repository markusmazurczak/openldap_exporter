package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	log "log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	kitlog "github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/exporter-toolkit/web"
	"github.com/tomcz/gotools/errgroup"
	"github.com/tomcz/gotools/maps"
	"github.com/tomcz/gotools/quiet"
	"github.com/urfave/cli/v2"
	"gopkg.in/ldap.v2"
)

const (
	promAddr          = "promAddr"
	ldapNet           = "ldapNet"
	ldapAddr          = "ldapAddr"
	ldapUser          = "ldapUser"
	ldapPass          = "ldapPass"
	interval          = "interval"
	metrics           = "metrPath"
	jsonLog           = "jsonLog"
	webCfgFile        = "webCfgFile"
	replicationObject = "replicationObject"
)

var showStop bool

func main() {
	flags := []cli.Flag{
		&cli.StringFlag{
			Name:    promAddr,
			Value:   ":9330",
			Usage:   "Bind address for Prometheus HTTP metrics server",
			EnvVars: []string{"PROM_ADDR"},
		},
		&cli.StringFlag{
			Name:    metrics,
			Value:   "/metrics",
			Usage:   "Path on which to expose Prometheus metrics",
			EnvVars: []string{"METRICS_PATH"},
		},
		&cli.StringFlag{
			Name:    ldapNet,
			Value:   "tcp",
			Usage:   "Network of OpenLDAP server",
			EnvVars: []string{"LDAP_NET"},
		},
		&cli.StringFlag{
			Name:    ldapAddr,
			Value:   "localhost:389",
			Usage:   "Address and port of OpenLDAP server",
			EnvVars: []string{"LDAP_ADDR"},
		},
		&cli.StringFlag{
			Name:    ldapUser,
			Usage:   "OpenLDAP bind username (optional)",
			EnvVars: []string{"LDAP_USER"},
		},
		&cli.StringFlag{
			Name:    ldapPass,
			Usage:   "OpenLDAP bind password (optional)",
			EnvVars: []string{"LDAP_PASS", "LDAP_PASSWORD"},
		},
		&cli.DurationFlag{
			Name:    interval,
			Value:   30 * time.Second,
			Usage:   "Scrape interval",
			EnvVars: []string{"INTERVAL"},
		},
		&cli.BoolFlag{
			Name:    jsonLog,
			Value:   false,
			Usage:   "Output logs in JSON format",
			EnvVars: []string{"JSON_LOG"},
		},
		&cli.StringFlag{
			Name:    webCfgFile,
			Usage:   "Prometheus metrics web config `FILE` (optional)",
			EnvVars: []string{"WEB_CFG_FILE"},
		},
		&cli.StringSliceFlag{
			Name:  replicationObject,
			Usage: "Object to watch replication upon (repeatable flag; use REPLICATION_OBJECTS env var with '|' separator for containers)",
		},
	}
	app := &cli.App{
		Name:            "openldap_exporter",
		Usage:           "Export OpenLDAP metrics to Prometheus",
		Version:         GetVersion(),
		HideHelpCommand: true,
		Flags:           flags,
		Action:          runMain,
	}
	if err := app.Run(os.Args); err != nil {
		log.Error("service failed", "err", err)
		os.Exit(1)
	}
	if showStop {
		log.Info("service stopped")
	}
}

func runMain(c *cli.Context) error {
	showStop = true

	if c.Bool(jsonLog) {
		lh := log.NewJSONHandler(os.Stderr, nil)
		log.SetDefault(log.New(lh))
	}
	log.Info("service starting")

	// Collect replication objects from CLI flags and the REPLICATION_OBJECTS env var.
	// The env var uses '|' as separator to avoid conflicts with commas in LDAP DNs.
	syncObjects := c.StringSlice(replicationObject)
	if replicationEnv := os.Getenv("REPLICATION_OBJECTS"); replicationEnv != "" {
		for _, obj := range strings.Split(replicationEnv, "|") {
			obj = strings.TrimSpace(obj)
			if obj != "" {
				syncObjects = append(syncObjects, obj)
			}
		}
	}

	server := NewMetricsServer(
		c.String(promAddr),
		c.String(metrics),
		c.String(webCfgFile),
	)

	scraper := &Scraper{
		Net:  c.String(ldapNet),
		Addr: c.String(ldapAddr),
		User: c.String(ldapUser),
		Pass: c.String(ldapPass),
		Tick: c.Duration(interval),
		Sync: syncObjects,
	}

	ctx, cancel := context.WithCancel(context.Background())
	group := errgroup.New()
	group.Go(func() error {
		defer cancel()
		return server.Start()
	})
	group.Go(func() error {
		defer cancel()
		scraper.Start(ctx)
		return nil
	})
	group.Go(func() error {
		defer func() {
			cancel()
			server.Stop()
		}()
		signalChan := make(chan os.Signal, 1)
		signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
		select {
		case <-signalChan:
			log.Info("shutdown received")
			return nil
		case <-ctx.Done():
			return nil
		}
	})
	return group.Wait()
}

// ===============================================================
// Metrics Scraper
// ===============================================================

const (
	baseDN    = "cn=Monitor"
	opsBaseDN = "cn=Operations,cn=Monitor"

	monitorCounterObject = "monitorCounterObject"
	monitorCounter       = "monitorCounter"

	monitoredObject = "monitoredObject"
	monitoredInfo   = "monitoredInfo"

	monitorOperation    = "monitorOperation"
	monitorOpCompleted  = "monitorOpCompleted"
	monitorOpInitiated  = "monitorOpInitiated"

	monitorReplicationFilter = "contextCSN"
	monitorReplication       = "monitorReplication"
)

type query struct {
	baseDN       string
	searchFilter string
	searchAttr   string
	metric       *prometheus.GaugeVec
	setData      func([]*ldap.Entry, *query)
}

var (
	monitoredObjectGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: "openldap",
			Name:      "monitored_object",
			Help:      help(baseDN, objectClass(monitoredObject), monitoredInfo),
		},
		[]string{"dn"},
	)
	monitorCounterObjectGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: "openldap",
			Name:      "monitor_counter_object",
			Help:      help(baseDN, objectClass(monitorCounterObject), monitorCounter),
		},
		[]string{"dn"},
	)
	monitorOperationGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: "openldap",
			Name:      "monitor_operation",
			Help:      help(opsBaseDN, objectClass(monitorOperation), monitorOpCompleted),
		},
		[]string{"dn"},
	)
	monitorOperationInitiatedGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: "openldap",
			Name:      "monitor_operation_initiated",
			Help:      help(opsBaseDN, objectClass(monitorOperation), monitorOpInitiated),
		},
		[]string{"dn"},
	)
	bindCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: "openldap",
			Name:      "bind",
			Help:      "successful vs unsuccessful ldap bind attempts",
		},
		[]string{"result"},
	)
	dialCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: "openldap",
			Name:      "dial",
			Help:      "successful vs unsuccessful ldap dial attempts",
		},
		[]string{"result"},
	)
	scrapeCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: "openldap",
			Name:      "scrape",
			Help:      "successful vs unsuccessful ldap scrape attempts",
		},
		[]string{"result"},
	)
	monitorReplicationGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: "openldap",
			Name:      "monitor_replication",
			Help:      help(baseDN, monitorReplication),
		},
		[]string{"id", "type"},
	)
	queries = []*query{
		{
			baseDN:       baseDN,
			searchFilter: objectClass(monitoredObject),
			searchAttr:   monitoredInfo,
			metric:       monitoredObjectGauge,
			setData:      setValue,
		},
		{
			baseDN:       baseDN,
			searchFilter: objectClass(monitorCounterObject),
			searchAttr:   monitorCounter,
			metric:       monitorCounterObjectGauge,
			setData:      setValue,
		},
		{
			baseDN:       opsBaseDN,
			searchFilter: objectClass(monitorOperation),
			searchAttr:   monitorOpCompleted,
			metric:       monitorOperationGauge,
			setData:      setValue,
		},
		{
			baseDN:       opsBaseDN,
			searchFilter: objectClass(monitorOperation),
			searchAttr:   monitorOpInitiated,
			metric:       monitorOperationInitiatedGauge,
			setData:      setValue,
		},
	}
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

func help(msg ...string) string {
	return strings.Join(msg, " ")
}

func objectClass(name string) string {
	return fmt.Sprintf("(objectClass=%v)", name)
}

func setValue(entries []*ldap.Entry, q *query) {
	for _, entry := range entries {
		val := entry.GetAttributeValue(q.searchAttr)
		if val == "" {
			// not every entry will have this attribute
			continue
		}
		num, err := strconv.ParseFloat(val, 64)
		if err != nil {
			// some of these attributes are not numbers
			continue
		}
		q.metric.WithLabelValues(entry.DN).Set(num)
	}
}

type Scraper struct {
	Net  string
	Addr string
	User string
	Pass string
	Tick time.Duration
	Sync []string
	log  *log.Logger
}

func (s *Scraper) Start(ctx context.Context) {
	s.log = log.With("component", "scraper")
	s.addReplicationQueries()
	address := fmt.Sprintf("%s://%s", s.Net, s.Addr)
	s.log.Info("starting monitor loop", "addr", address)
	s.checkMonitorBackend()
	ticker := time.NewTicker(s.Tick)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.scrape()
		case <-ctx.Done():
			return
		}
	}
}

// checkMonitorBackend performs a one-time base-scope search on cn=Monitor at
// startup to verify the monitor backend is reachable. Logs a clear diagnostic
// message if it is not, so the user knows why all subsequent scrapes will fail.
func (s *Scraper) checkMonitorBackend() {
	conn, err := dialLDAP(s.Net, s.Addr)
	if err != nil {
		s.log.Warn("startup check: dial failed", "err", err)
		return
	}
	defer conn.Close()

	if s.User != "" && s.Pass != "" {
		if err = conn.Bind(s.User, s.Pass); err != nil {
			s.log.Warn("startup check: bind failed — monitor queries will fail", "err", err)
			return
		}
	}

	req := ldap.NewSearchRequest(
		baseDN, ldap.ScopeBaseObject, ldap.NeverDerefAliases, 0, 0, false,
		"(objectClass=*)", []string{"cn"}, nil,
	)
	_, err = conn.Search(req)
	if err != nil {
		if isNoSuchObject(err) {
			s.log.Error("startup check: cn=Monitor not found — enable the OpenLDAP monitor backend (add 'database monitor' to slapd.conf) and ensure the bind user has read access")
		} else {
			s.log.Warn("startup check: cn=Monitor search failed", "err", err)
		}
	} else {
		s.log.Info("startup check: cn=Monitor is accessible")
	}
}

func (s *Scraper) addReplicationQueries() {
	for _, q := range s.Sync {
		queries = append(queries,
			&query{
				baseDN:       q,
				searchFilter: objectClass("*"),
				searchAttr:   monitorReplicationFilter,
				metric:       monitorReplicationGauge,
				setData:      s.setReplicationValue,
			},
		)
	}
}

func (s *Scraper) setReplicationValue(entries []*ldap.Entry, q *query) {
	for _, entry := range entries {
		val := entry.GetAttributeValue(q.searchAttr)
		if val == "" {
			// not every entry will have this attribute
			continue
		}
		ll := s.log.With(
			"filter", q.searchFilter,
			"attr", q.searchAttr,
			"value", val,
		)
		valueBuffer := strings.Split(val, "#")
		gt, err := time.Parse("20060102150405.999999Z", valueBuffer[0])
		if err != nil {
			ll.Warn("unexpected gt value", "err", err)
			continue
		}
		count, err := strconv.ParseFloat(valueBuffer[1], 64)
		if err != nil {
			ll.Warn("unexpected count value", "err", err)
			continue
		}
		sid := valueBuffer[2]
		mod, err := strconv.ParseFloat(valueBuffer[3], 64)
		if err != nil {
			ll.Warn("unexpected mod value", "err", err)
			continue
		}
		q.metric.WithLabelValues(sid, "gt").Set(float64(gt.Unix()))
		q.metric.WithLabelValues(sid, "count").Set(count)
		q.metric.WithLabelValues(sid, "mod").Set(mod)
	}
}

func (s *Scraper) scrape() {
	conn, err := dialLDAP(s.Net, s.Addr)
	if err != nil {
		s.log.Error("dial failed", "err", err)
		dialCounter.WithLabelValues("fail").Inc()
		return
	}
	dialCounter.WithLabelValues("ok").Inc()
	defer conn.Close()

	if s.User != "" && s.Pass != "" {
		err = conn.Bind(s.User, s.Pass)
		if err != nil {
			s.log.Error("bind failed", "err", err)
			bindCounter.WithLabelValues("fail").Inc()
			return
		}
		bindCounter.WithLabelValues("ok").Inc()
	}

	scrapeRes := "ok"
	for _, q := range queries {
		if err = scrapeQuery(conn, q); err != nil {
			s.log.Warn("query failed", "baseDN", q.baseDN, "filter", q.searchFilter, "err", err)
			if isNoSuchObject(err) {
				s.log.Warn("base DN not found — check that the OpenLDAP monitor backend is enabled and the bind user has read access", "baseDN", q.baseDN)
			}
			scrapeRes = "fail"
		}
	}
	scrapeCounter.WithLabelValues(scrapeRes).Inc()
}

// dialLDAP connects to the LDAP server. addr may be a plain host:port or a
// full URL (ldap://host:port or ldaps://host:port). For ldaps:// the connection
// is upgraded to TLS. The net parameter is used only when addr has no scheme.
func dialLDAP(network, addr string) (*ldap.Conn, error) {
	if u, err := url.Parse(addr); err == nil && u.Scheme != "" {
		host := u.Host
		switch u.Scheme {
		case "ldaps":
			return ldap.DialTLS("tcp", host, &tls.Config{})
		case "ldap":
			return ldap.Dial("tcp", host)
		}
	}
	return ldap.Dial(network, addr)
}

// isNoSuchObject returns true when the LDAP server responds with result code 32
// (No Such Object), which typically means the base DN does not exist or the
// bind user has no ACL access to it.
func isNoSuchObject(err error) bool {
	if ldapErr, ok := err.(*ldap.Error); ok {
		return ldapErr.ResultCode == ldap.LDAPResultNoSuchObject
	}
	return false
}

func scrapeQuery(conn *ldap.Conn, q *query) error {
	req := ldap.NewSearchRequest(
		q.baseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		q.searchFilter, []string{q.searchAttr}, nil,
	)
	sr, err := conn.Search(req)
	if err != nil {
		return err
	}
	q.setData(sr.Entries, q)
	return nil
}

// ===============================================================
// Metrics server
// ===============================================================

var commit string
var tag string

func GetVersion() string {
	return fmt.Sprintf("%s (%s)", tag, commit)
}

type Server struct {
	server  *http.Server
	logger  *log.Logger
	cfgPath string
}

func NewMetricsServer(bindAddr, metricsPath, tlsConfigPath string) *Server {
	mux := http.NewServeMux()
	mux.Handle(metricsPath, promhttp.Handler())
	mux.HandleFunc("/version", showVersion)
	return &Server{
		server:  &http.Server{Addr: bindAddr, Handler: mux},
		logger:  log.With("component", "server"),
		cfgPath: tlsConfigPath,
	}
}

func showVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, GetVersion())
}

func (s *Server) Start() error {
	s.logger.Info("starting http listener", "addr", s.server.Addr)
	err := web.ListenAndServe(s.server, s.cfgPath, kitlog.LoggerFunc(s.adaptor))
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Stop() {
	quiet.CloseWithTimeout(s.server.Shutdown, 100*time.Millisecond)
}

func (s *Server) adaptor(kvs ...interface{}) error {
	if len(kvs) == 0 {
		return nil
	}
	if len(kvs)%2 != 0 {
		kvs = append(kvs, nil)
	}
	fields := make(map[string]any)
	for i := 0; i < len(kvs); i += 2 {
		key := fmt.Sprint(kvs[i])
		fields[key] = kvs[i+1]
	}
	var msg string
	if val, ok := fields["msg"]; ok {
		delete(fields, "msg")
		msg = fmt.Sprint(val)
	}
	var level string
	if val, ok := fields["level"]; ok {
		delete(fields, "level")
		level = fmt.Sprint(val)
	}
	var args []any
	for _, e := range maps.SortedEntries(fields) {
		args = append(args, e.Key, e.Val)
	}
	ll := s.logger.With(args...)
	switch level {
	case "error":
		ll.Error(msg)
	case "warn":
		ll.Warn(msg)
	case "debug":
		ll.Debug(msg)
	default:
		ll.Info(msg)
	}
	return nil
}
