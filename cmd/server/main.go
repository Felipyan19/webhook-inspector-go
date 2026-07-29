package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/Felipyan19/webhook-inspector-go/internal/app"
	"github.com/Felipyan19/webhook-inspector-go/internal/store"
)

func main() {
	addr := env("ADDR", ":8080")
	dbPath := env("DATABASE_PATH", "./data/webhooks.db")

	db, err := store.Open(dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	handler := app.New(db)
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("Webhook Inspector listening on %s", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
