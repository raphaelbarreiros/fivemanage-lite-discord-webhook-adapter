package main

import (
	"context"
	"errors"
	"flag"
	"local/fivemanage-discord-adapter/internal/app"
	"local/fivemanage-discord-adapter/internal/config"
	"local/fivemanage-discord-adapter/internal/queue"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if run() != nil {
		os.Exit(1)
	}
}
func run() error {
	path := flag.String("config", "config.json", "trusted configuration file")
	health := flag.String("healthcheck", "", "check an admin health URL then exit")
	flag.Parse()
	if *health != "" {
		c := http.Client{Timeout: 2 * time.Second}
		r, err := c.Get(*health)
		if err != nil {
			return err
		}
		r.Body.Close()
		if r.StatusCode != 200 {
			return errors.New("unhealthy")
		}
		return nil
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load(*path)
	if err != nil {
		logger.Error(err.Error())
		return err
	}
	q, err := queue.Open(context.Background(), cfg.DataDir, cfg.Queue)
	if err != nil {
		logger.Error("cannot initialize local queue")
		return err
	}
	defer q.Close()
	stats, err := q.Stats(context.Background())
	if err != nil {
		logger.Error("cannot inspect local queue")
		return err
	}
	logger.Info("queue opened", "sqlite", stats.SQLiteVersion, "pending", stats.Messages, "journal_mode", "wal", "synchronous", "FULL", "auto_vacuum", "INCREMENTAL")
	a := app.New(cfg, q, logger)
	defer a.Close()
	ingress, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		logger.Error("cannot bind receiver")
		return err
	}
	admin, err := net.Listen("tcp", cfg.AdminListen)
	if err != nil {
		ingress.Close()
		logger.Error("cannot bind admin")
		return err
	}
	// net/http's default panic logger can include request data. Keep only a fixed diagnostic.
	errorLog := log.New(redactedWriter{logger}, "", 0)
	server := &http.Server{Handler: a.Handler(), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: cfg.AdmissionTimeout.Value(), WriteTimeout: cfg.AdmissionTimeout.Value() + time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192, ErrorLog: errorLog}
	adminServer := &http.Server{Handler: a.AdminHandler(), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 4096, ErrorLog: errorLog}
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); a.RunWorker(workerCtx) }()
	failures := make(chan error, 2)
	go func() { failures <- server.Serve(ingress) }()
	go func() { failures <- adminServer.Serve(admin) }()
	signalCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger.Info("receiver started")
	select {
	case <-signalCtx.Done():
	case err = <-failures:
		logger.Error("HTTP server stopped")
	}
	a.StopAdmissions()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout.Value())
	defer cancel()
	if e := server.Shutdown(shutdownCtx); e != nil {
		server.Close()
		err = e
	}
	cancelWorker()
	<-workerDone
	if e := adminServer.Shutdown(shutdownCtx); e != nil {
		adminServer.Close()
		err = e
	}
	logger.Info("shutdown complete; unconfirmed messages preserved")
	return err
}

type redactedWriter struct{ logger *slog.Logger }

func (w redactedWriter) Write(b []byte) (int, error) {
	w.logger.Error("HTTP internal error (details redacted)")
	return len(b), nil
}
