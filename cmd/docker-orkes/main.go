package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"docker-orkes/internal/server"
)

func main() {
	var (
		addr   = flag.String("addr", getEnv("ADDR", ":8080"), "HTTP server address")
		dbPath = flag.String("db", getEnv("DB_PATH", "docker-orkes.db"), "SQLite database file path")
		pass   = flag.String("pass", getEnv("MASTER_PASSWORD", "admin"), "Master password for dashboard access")
	)
	flag.Parse()

	srv, err := server.New(server.Config{
		Addr:           *addr,
		DBPath:         *dbPath,
		MasterPassword: *pass,
	})
	if err != nil {
		log.Fatalf("Failed to initialize server: %v", err)
	}
	defer srv.Close()

	httpServer := &http.Server{
		Addr:         *addr,
		Handler:      srv.Handler(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	fmt.Println("==================================================")
	fmt.Printf("🐳 Docker Orkes Control Plane started on %s\n", *addr)
	fmt.Printf("💾 Database: %s\n", *dbPath)
	fmt.Println("🔑 Control Plane SSH Public Key:")
	fmt.Print(srv.PublicKeySSH())
	fmt.Println("==================================================")

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	<-sigChan
	fmt.Println("\nShutting down gracefully...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("Shutdown error: %v", err)
	}
	fmt.Println("Server stopped.")
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
