package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Time limits against slow or stalled clients (Slowloris).
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 60 * time.Second
	shutdownTimeout   = 5 * time.Second
)

type Server struct {
	server *http.Server
	logger *slog.Logger
}

func NewMetricsServer(bindAddr, metricsPath string) *Server {
	mux := http.NewServeMux()
	mux.Handle(metricsPath, promhttp.Handler())
	mux.HandleFunc("/version", showVersion)
	return &Server{
		server: &http.Server{
			Addr:              bindAddr,
			Handler:           mux,
			ReadHeaderTimeout: readHeaderTimeout,
			ReadTimeout:       readTimeout,
			WriteTimeout:      writeTimeout,
			IdleTimeout:       idleTimeout,
		},
		logger: slog.With("component", "server"),
	}
}

func showVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, GetVersion())
}

func (s *Server) Start() error {
	s.logger.Info("starting http listener", "addr", s.server.Addr)
	if err := s.server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return s.server.Shutdown(ctx)
}

// healthcheck probes the local metrics endpoint (container health check without a shell).
// A bind address without host (":9330") means the local interface.
func healthcheck(bindAddr, metricsPath string, timeout time.Duration) error {
	host, port, err := net.SplitHostPort(bindAddr)
	if err != nil {
		return fmt.Errorf("invalid bind address %q: %w", bindAddr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + metricsPath)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("metrics endpoint returned %s", resp.Status)
	}
	return nil
}
