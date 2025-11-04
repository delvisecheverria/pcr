package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"pulse/pkg/engine"
	"pulse/pkg/recorder"
)

var (
	activeRecorder *recorder.Recorder
	recorderLock   sync.Mutex
	logSubscribers = make(map[chan string]bool)
	logMu          sync.Mutex
)

// broadcastLog envia mensajes a todos los clientes conectados via SSE
func broadcastLog(msg string) {
	logMu.Lock()
	defer logMu.Unlock()
	for ch := range logSubscribers {
		select {
		case ch <- msg:
		default:
		}
	}
}

// findAvailablePort busca el siguiente puerto libre a partir del indicado
func findAvailablePort(startPort int) string {
	for port := startPort; port < startPort+10; port++ {
		addr := fmt.Sprintf(":%d", port)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			ln.Close()
			return addr
		}
	}
	return fmt.Sprintf(":%d", startPort) // fallback
}

func main() {
	var rootCmd = &cobra.Command{
		Use:   "pulse",
		Short: "Pulse - Visual, fast, human-friendly load testing (Go)",
		Long:  "Pulse - Visual, fast, human-friendly load testing tool built in Go.",
	}

	// ---------------------------------------------------------------------
	// RUN COMMAND
	// ---------------------------------------------------------------------
	var runCmd = &cobra.Command{
		Use:   "run <file>",
		Short: "Run a Pulse scenario file",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			file := args[0]
			if _, err := os.Stat(file); os.IsNotExist(err) {
				fmt.Printf("❌ File not found: %s\n", file)
				os.Exit(1)
			}
			fmt.Printf("🚀 Running scenario: %s\n", file)
			if err := engine.Run(file); err != nil {
				fmt.Println("Error running scenario:", err)
				os.Exit(1)
			}
		},
	}

	// ---------------------------------------------------------------------
	// RECORD COMMAND (CLI)
	// ---------------------------------------------------------------------
	var recordAddr, recordOut string
	var recordCmd = &cobra.Command{
		Use:   "record",
		Short: "Start the Pulse recorder (proxy) and generate a scenario",
		Long:  "Start a local HTTP(S) proxy recorder. Configure your browser/app proxy to localhost:<port> and perform actions. Press Ctrl+C to stop.",
		Run: func(cmd *cobra.Command, args []string) {
			rec := recorder.New(recordAddr, recordOut)
			if err := rec.Start(); err != nil {
				fmt.Println("Recorder error:", err)
				os.Exit(1)
			}
		},
	}
	recordCmd.Flags().StringVarP(&recordAddr, "addr", "a", ":8888", "Address to listen on (e.g. :8888)")
	recordCmd.Flags().StringVarP(&recordOut, "out", "o", "examples", "Output directory for recorded YAML files")

	// ---------------------------------------------------------------------
	// SERVE COMMAND — Full Web UI + API
	// ---------------------------------------------------------------------
	var serveCmd = &cobra.Command{
		Use:   "serve",
		Short: "Start Pulse web UI and REST API",
		Run: func(cmd *cobra.Command, args []string) {
			port := findAvailablePort(5050)
			fmt.Printf("⚡ Starting Pulse backend & UI at http://localhost%s\n", port)

			mux := http.NewServeMux()
			var rec *recorder.Recorder
			var isRunning bool
			var mu sync.Mutex

			// --- API: Start Recorder ---
			mux.HandleFunc("/api/start-record", func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()

				w.Header().Set("Content-Type", "application/json")

				if isRunning {
					w.WriteHeader(http.StatusConflict)
					json.NewEncoder(w).Encode(map[string]string{
						"error":   "conflict",
						"message": "Recorder already running",
					})
					return
				}

				rec = recorder.New(":8888", "examples")
				isRunning = true
				broadcastLog("✅ Recorder started on :8888")

				// 🔥 Escucha los eventos emitidos por el recorder
				go func() {
					for ev := range rec.Events {
						eventData := map[string]interface{}{
							"method":   ev.Method,
							"url":      ev.URL,
							"status":   ev.Status,
							"time":     ev.Time,
							"headers":  ev.Headers,
							"body":     ev.Body,
							"response": ev.Response,
						}
						data, _ := json.Marshal(eventData)
						broadcastLog(string(data))
					}
				}()

				// Inicia el recorder en background
				go func() {
					if err := rec.Start(); err != nil {
						broadcastLog(fmt.Sprintf("❌ Recorder error: %v", err))
					}
					mu.Lock()
					isRunning = false
					mu.Unlock()
					broadcastLog("🟢 Recorder stopped (finished or interrupted)")
				}()

				json.NewEncoder(w).Encode(map[string]string{
					"message": "✅ Recorder started on :8888 — configure your proxy to localhost:8888",
				})
			})

			// --- API: Stop Recorder ---
			mux.HandleFunc("/api/stop-record", func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()

				w.Header().Set("Content-Type", "application/json")

				if !isRunning || rec == nil {
					w.WriteHeader(http.StatusBadRequest)
					json.NewEncoder(w).Encode(map[string]string{
						"error":   "not_running",
						"message": "Recorder not running",
					})
					return
				}

				rec.Stop()
				isRunning = false
				broadcastLog("📁 Recorder stopped and YAML written to ./examples")

				json.NewEncoder(w).Encode(map[string]string{
					"message": "🛑 Recorder stopped and YAML written to ./examples",
				})
			})

			// --- API: Status ---
			mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
				status := map[string]interface{}{
					"running": isRunning,
					"message": func() string {
						if isRunning {
							return "Recorder is running on :8888"
						}
						return "Recorder is idle"
					}(),
					"status": "ok",
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(status)
			})

			// --- API: List Recordings ---
			mux.HandleFunc("/api/recordings", func(w http.ResponseWriter, r *http.Request) {
				files, err := os.ReadDir("examples")
				if err != nil {
					http.Error(w, "Cannot read recordings directory", http.StatusInternalServerError)
					return
				}
				var list []string
				for _, f := range files {
					if !f.IsDir() && strings.HasSuffix(f.Name(), ".yaml") {
						list = append(list, f.Name())
					}
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(list)
			})

			// --- API: Logs Stream (SSE) ---
			mux.HandleFunc("/api/logs", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Connection", "keep-alive")

				ch := make(chan string, 10)
				logMu.Lock()
				logSubscribers[ch] = true
				logMu.Unlock()

				broadcastLog("👋 New client connected to /api/logs")

				defer func() {
					logMu.Lock()
					delete(logSubscribers, ch)
					logMu.Unlock()
					close(ch)
					broadcastLog("👋 Client disconnected from /api/logs")
				}()

				ticker := time.NewTicker(5 * time.Second)
				defer ticker.Stop()

				for {
					select {
					case <-r.Context().Done():
						return
					case msg := <-ch:
						fmt.Fprintf(w, "data: %s\n\n", msg)
						if f, ok := w.(http.Flusher); ok {
							f.Flush()
						}
					case <-ticker.C:
						fmt.Fprintf(w, "data: 🔄 heartbeat %s\n\n", time.Now().Format("15:04:05"))
						if f, ok := w.(http.Flusher); ok {
							f.Flush()
						}
					}
				}
			})

			// --- Serve recordings (examples) ---
			mux.Handle("/examples/", http.StripPrefix("/examples/", http.FileServer(http.Dir("examples"))))

			// --- Serve UI ---
			uiDir := "./ui"
			if _, err := os.Stat(uiDir); os.IsNotExist(err) {
				execDir, _ := os.Getwd()
				uiDir = fmt.Sprintf("%s/ui", execDir)
			}
			fmt.Printf("📂 Serving UI from: %s\n", uiDir)

			fs := http.FileServer(http.Dir(uiDir))
			mux.Handle("/", fs)

			// --- Add CORS middleware ---
			corsMux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
				if r.Method == http.MethodOptions {
					w.WriteHeader(http.StatusOK)
					return
				}
				mux.ServeHTTP(w, r)
			})

			// --- Start Server ---
			srv := &http.Server{Addr: port, Handler: corsMux}

			stop := make(chan os.Signal, 1)
			signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
			go func() {
				<-stop
				fmt.Println("\n🧹 Shutting down Pulse...")
				broadcastLog("🧹 Server shutting down gracefully")
				srv.Close()
			}()

			fmt.Printf("✅ Pulse running on http://localhost%s\n", port)
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				fmt.Println("Server error:", err)
				os.Exit(1)
			}
		},
	}

	rootCmd.AddCommand(runCmd, recordCmd, serveCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
}
