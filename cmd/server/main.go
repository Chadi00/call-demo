package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chadiek/call-demo/internal/config"
	httpserver "github.com/chadiek/call-demo/internal/httpserver"
)

func main() {
	// Include sub-second precision in all log timestamps
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)

	cfg := config.Load()

	srv := httpserver.New(cfg)

	server := &http.Server{
		Addr:              cfg.HTTPAddress,
		Handler:           srv.Router,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Start server in background
	serverErrors := make(chan error, 1)
	go func() {
		log.Printf("server listening on %s", cfg.HTTPAddress)
		serverErrors <- server.ListenAndServe()
	}()

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErrors:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	case sig := <-sigChan:
		log.Printf("shutdown signal received: %v", sig)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
		_ = server.Close()
	}
}
