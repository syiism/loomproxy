package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"loomproxy/app"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.Printf("Received signal: %v, shutting down...", sig)
		cancel()
	}()

	if err := app.Run(ctx); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
