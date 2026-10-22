package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"docker-orkes/internal/logger"
	"docker-orkes/internal/server"
)

const defaultPidFile = "docker-orkes.pid"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "stop":
			handleStop(os.Args[2:])
			return
		case "restart":
			handleRestart(os.Args[2:])
			return
		case "status":
			handleStatus(os.Args[2:])
			return
		case "start":
			// Strip "start" subcommand so flags parse normally
			os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
		}
	}

	runServer()
}

func runServer() {
	var (
		addr     = flag.String("addr", getEnv("ADDR", ":8080"), "HTTP server address")
		dbPath   = flag.String("db", getEnv("DB_PATH", "docker-orkes.db"), "SQLite database file path")
		pass     = flag.String("pass", getEnv("MASTER_PASSWORD", "admin"), "Master password for dashboard access")
		pidFile  = flag.String("pid", defaultPidFile, "Path to PID file")
		daemon   = flag.Bool("d", false, "Run in background (daemon mode)")
		logFile  = flag.String("log-file", getEnv("LOG_FILE", "docker-orkes.log"), "Path to rotating log file (empty to disable)")
		logLevel = flag.String("log-level", getEnv("LOG_LEVEL", "info"), "Log level (debug, info, warn, error)")
		tlsCert      = flag.String("tls-cert", getEnv("TLS_CERT", ""), "Path to TLS certificate file (enables HTTPS)")
		tlsKey       = flag.String("tls-key", getEnv("TLS_KEY", ""), "Path to TLS private key file")
		gitMirrorDir = flag.String("git-mirror-dir", getEnv("GIT_MIRROR_DIR", "data/git-mirror"), "Path to Git mirror directory")
		apiToken     = flag.String("api-token", getEnv("API_TOKEN", ""), "API token for automated CI/CD deployment calls")
	)
	flag.Parse()

	if *daemon {
		runDaemon()
		return
	}

	// Initialize Diagnostic Logger and Telemetry Broadcaster (ADR 0017)
	logRes, logErr := logger.Setup(logger.Config{
		Level:    *logLevel,
		FilePath: *logFile,
	})
	if logErr == nil && logRes.Closer != nil {
		defer logRes.Closer.Close()
	}

	// Check if already running with this PID file
	if existingPid, err := readPidFile(*pidFile); err == nil && isProcessAlive(existingPid) && existingPid != os.Getpid() {
		log.Fatalf("Error: Docker Orkes is already running (PID: %d). Stop it first or specify a different -pid file.", existingPid)
	}

	// Write PID file
	if err := writePidFile(*pidFile, os.Getpid()); err != nil {
		log.Printf("Warning: failed to write PID file: %v", err)
	}
	defer removePidFile(*pidFile)

	srv, err := server.New(server.Config{
		Addr:           *addr,
		DBPath:         *dbPath,
		MasterPassword: *pass,
		GitMirrorDir:   *gitMirrorDir,
		APIToken:       *apiToken,
	})
	if err != nil {
		log.Fatalf("Failed to initialize server: %v", err)
	}
	defer srv.Close()

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	proto := "http"
	if *tlsCert != "" && *tlsKey != "" {
		proto = "https"
	}
	fmt.Println("==================================================")
	fmt.Printf("🐳 Docker Orkes Control Plane started on %s://%s (PID: %d)\n", proto, *addr, os.Getpid())
	fmt.Printf("💾 Database: %s\n", *dbPath)
	fmt.Println("🔑 Control Plane SSH Public Key:")
	fmt.Print(srv.PublicKeySSH())
	fmt.Println("==================================================")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		var err error
		if *tlsCert != "" && *tlsKey != "" {
			err = httpServer.ListenAndServeTLS(*tlsCert, *tlsKey)
		} else {
			err = httpServer.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			_ = removePidFile(*pidFile)
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

func handleStop(args []string) {
	pidFile := extractPidFile(args)
	pid, err := readPidFile(pidFile)
	if err != nil {
		fmt.Printf("❌ Cannot find running process or PID file '%s': %v\n", pidFile, err)
		os.Exit(1)
	}

	if !isProcessAlive(pid) {
		fmt.Printf("⚠️ Process %d is not running. Removing stale PID file.\n", pid)
		_ = removePidFile(pidFile)
		return
	}

	fmt.Printf("🛑 Stopping Docker Orkes (PID: %d)...\n", pid)
	if err := killProcess(pid, syscall.SIGTERM); err != nil {
		fmt.Printf("❌ Failed to send stop signal: %v\n", err)
		os.Exit(1)
	}

	// Wait for process to exit
	for i := 0; i < 30; i++ {
		time.Sleep(200 * time.Millisecond)
		if !isProcessAlive(pid) {
			_ = removePidFile(pidFile)
			fmt.Println("✅ Docker Orkes stopped successfully.")
			return
		}
	}

	// Force kill if graceful timeout expires
	fmt.Printf("⚠️ Graceful shutdown timed out. Force killing PID %d...\n", pid)
	_ = killProcess(pid, syscall.SIGKILL)
	_ = removePidFile(pidFile)
	fmt.Println("✅ Docker Orkes force stopped.")
}

func handleRestart(args []string) {
	pidFile := extractPidFile(args)
	pid, err := readPidFile(pidFile)
	if err == nil && isProcessAlive(pid) {
		fmt.Printf("🛑 Stopping existing instance (PID: %d)...\n", pid)
		_ = killProcess(pid, syscall.SIGTERM)
		stopped := false
		for i := 0; i < 30; i++ {
			time.Sleep(200 * time.Millisecond)
			if !isProcessAlive(pid) {
				stopped = true
				break
			}
		}
		if !stopped {
			fmt.Printf("⚠️ Graceful stop timed out. Force killing PID %d...\n", pid)
			_ = killProcess(pid, syscall.SIGKILL)
			for i := 0; i < 10; i++ {
				time.Sleep(100 * time.Millisecond)
				if !isProcessAlive(pid) {
					break
				}
			}
		}
		_ = removePidFile(pidFile)
	}

	fmt.Println("🔄 Starting Docker Orkes...")
	cmdArgs := []string{"start"}
	cmdArgs = append(cmdArgs, args...)

	execPath, err := os.Executable()
	if err != nil {
		execPath = os.Args[0]
	}

	cmd := exec.Command(execPath, cmdArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		log.Fatalf("Restart failed: %v", err)
	}
}

func handleStatus(args []string) {
	pidFile := extractPidFile(args)
	pid, err := readPidFile(pidFile)
	if err != nil || !isProcessAlive(pid) {
		fmt.Printf("⚪ Docker Orkes is NOT running.\n")
		return
	}

	fmt.Printf("🟢 Docker Orkes is RUNNING (PID: %d, PID File: %s)\n", pid, pidFile)
}

func extractPidFile(args []string) string {
	for i, arg := range args {
		if (arg == "-pid" || arg == "--pid") && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(arg, "-pid=") || strings.HasPrefix(arg, "--pid=") {
			parts := strings.SplitN(arg, "=", 2)
			if len(parts) == 2 && parts[1] != "" {
				return parts[1]
			}
		}
	}
	return defaultPidFile
}

func runDaemon() {
	execPath, err := os.Executable()
	if err != nil {
		execPath = os.Args[0]
	}

	var childArgs []string
	for _, a := range os.Args[1:] {
		if a != "-d" && a != "--d" {
			childArgs = append(childArgs, a)
		}
	}

	cmd := exec.Command(execPath, childArgs...)
	cmd.SysProcAttr = setupDaemonSysProcAttr()

	if err := cmd.Start(); err != nil {
		log.Fatalf("Failed to start daemon: %v", err)
	}

	fmt.Printf("🚀 Docker Orkes daemon started (PID: %d)\n", cmd.Process.Pid)
}

func writePidFile(path string, pid int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(pid)), 0644)
}

func readPidFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

func removePidFile(path string) error {
	return os.Remove(path)
}

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := killProcess(pid, 0)
	return err == nil
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
