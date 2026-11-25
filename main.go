// openldap-exporter reads cn=Monitor of an OpenLDAP server and exposes the values as
// Prometheus metrics. Derived from tomcz/openldap_exporter (MIT, see LICENSE.md);
// maintained at github.com/markusmazurczak/openldap_exporter.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/urfave/cli/v2"
	"golang.org/x/sync/errgroup"
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
	replicationObject = "replicationObject"
	healthcheckFlag   = "healthcheck"
)

// Set at build time via -ldflags.
var (
	commit = "unknown"
	tag    = "dev"
)

func GetVersion() string {
	return fmt.Sprintf("%s (%s)", tag, commit)
}

func main() {
	app := &cli.App{
		Name:            "openldap-exporter",
		Usage:           "Export OpenLDAP metrics to Prometheus",
		Version:         GetVersion(),
		HideHelpCommand: true,
		Flags:           appFlags(),
		Action:          runMain,
	}
	if err := app.Run(os.Args); err != nil {
		slog.Error("service failed", "err", err)
		os.Exit(1)
	}
}

func appFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: promAddr, Value: ":9330", Usage: "Bind address for Prometheus HTTP metrics server", EnvVars: []string{"PROM_ADDR"}},
		&cli.StringFlag{Name: metrics, Value: "/metrics", Usage: "Path on which to expose Prometheus metrics", EnvVars: []string{"METRICS_PATH"}},
		&cli.StringFlag{Name: ldapNet, Value: "tcp", Usage: "Network of OpenLDAP server (tcp or unix), used when the address has no scheme", EnvVars: []string{"LDAP_NET"}},
		&cli.StringFlag{Name: ldapAddr, Value: "localhost:389", Usage: "Address of OpenLDAP server: host:port, ldap://, ldaps:// or ldapi:// URL", EnvVars: []string{"LDAP_ADDR"}},
		&cli.StringFlag{Name: ldapUser, Usage: "OpenLDAP bind username (optional)", EnvVars: []string{"LDAP_USER"}},
		&cli.StringFlag{Name: ldapPass, Usage: "OpenLDAP bind password (optional)", EnvVars: []string{"LDAP_PASS", "LDAP_PASSWORD"}},
		&cli.DurationFlag{Name: interval, Value: 30 * time.Second, Usage: "Scrape interval", EnvVars: []string{"INTERVAL"}},
		&cli.BoolFlag{Name: jsonLog, Usage: "Output logs in JSON format", EnvVars: []string{"JSON_LOG"}},
		&cli.StringSliceFlag{Name: replicationObject, Usage: "Object to watch replication upon (repeatable; REPLICATION_OBJECTS env var with '|' separator)"},
		&cli.BoolFlag{Name: healthcheckFlag, Usage: "Check that the local metrics endpoint answers, then exit (for container health checks)"},
	}
}

func runMain(c *cli.Context) error {
	if c.Bool(healthcheckFlag) {
		return healthcheck(c.String(promAddr), c.String(metrics), 3*time.Second)
	}
	// The exporter-toolkit web config (TLS/auth) is gone. Refuse to start rather than
	// silently serving metrics without the protection the operator expects.
	if os.Getenv("WEB_CFG_FILE") != "" {
		return errors.New("WEB_CFG_FILE is no longer supported (metrics are served on the internal network only)")
	}
	if c.Bool(jsonLog) {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	}
	slog.Info("service starting", "version", GetVersion())

	server := NewMetricsServer(c.String(promAddr), c.String(metrics))
	scraper := &Scraper{
		Net:  c.String(ldapNet),
		Addr: c.String(ldapAddr),
		User: c.String(ldapUser),
		Pass: c.String(ldapPass),
		Tick: c.Duration(interval),
		Sync: parseReplicationObjects(c.StringSlice(replicationObject), os.Getenv("REPLICATION_OBJECTS")),
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	group, gctx := errgroup.WithContext(ctx)
	group.Go(server.Start)
	group.Go(func() error {
		scraper.Start(gctx)
		return nil
	})
	group.Go(func() error {
		<-gctx.Done()
		return server.Stop()
	})
	err := group.Wait()
	slog.Info("service stopped")
	return err
}

// parseReplicationObjects combines the flag values with REPLICATION_OBJECTS. The env var
// uses '|' as separator because DNs contain commas.
func parseReplicationObjects(fromFlags []string, fromEnv string) []string {
	objects := append([]string{}, fromFlags...)
	for _, obj := range strings.Split(fromEnv, "|") {
		if obj = strings.TrimSpace(obj); obj != "" {
			objects = append(objects, obj)
		}
	}
	return objects
}
