package main

import (
	"context"
	orders "entradasya.local/orders"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	config, err := orders.ConfigFromEnv()
	if err != nil {
		logger.Error("invalid_configuration")
		os.Exit(1)
	}
	service, err := orders.New(config, orders.NewHTTPBackend(config), nil)
	if err != nil {
		logger.Error("initialization_failed")
		os.Exit(1)
	}
	server := &http.Server{Addr: net.JoinHostPort("127.0.0.1", strconv.Itoa(config.Port)), Handler: service.Handler(logger), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 8 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 * 1024}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
		}
	}()
	logger.Info("orders_starting", "port", config.Port)
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		<-shutdownDone
	}
	service.Close()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server_failed")
		os.Exit(1)
	}
}
