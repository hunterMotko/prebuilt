package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/hunterMotko/prebuilt/database"
	"github.com/hunterMotko/prebuilt/handlers"
)

// main is process lifecycle only — configuration, storage, signals, shutdown.
// The server's wiring lives in newServer (server.go) so it can be built and
// exercised without starting a listener or mutating the environment.
func main() {
	_ = godotenv.Load()

	cfg := loadConfig()

	database.Init(cfg.DBPath)

	e, err := newServer(cfg)
	if err != nil {
		log.Fatalf("failed to build server: %v", err)
	}

	// Graceful shutdown: on SIGINT/SIGTERM (Ctrl-C, systemd stop, deploy
	// restart), stop accepting new connections and give in-flight requests up
	// to 10 seconds to finish instead of dropping them mid-response.
	go func() {
		if err := e.Start(":" + cfg.Port); err != nil && err != http.ErrServerClosed {
			e.Logger.Fatal("server error: ", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.Shutdown(ctx); err != nil {
		e.Logger.Fatal(err)
	}

	// Notification emails are sent in background goroutines that Shutdown
	// knows nothing about, so without this a deploy landing between "lead
	// saved" and "mail sent" would drop the notification silently.
	handlers.WaitForEmails(ctx)
}
