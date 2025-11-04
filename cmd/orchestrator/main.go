package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"pulse/pkg/engine"
)

// --- Broker para manejar clientes SSE ---
type sseBroker struct {
	clients map[chan []byte]bool
	mu      sync.Mutex
}

func newSSEBroker() *sseBroker {
	return &sseBroker{
		clients: make(map[chan []byte]bool),
	}
}

func (b *sseBroker) addClient(ch chan []byte) {
	b.mu.Lock()
	b.clients[ch] = true
	b.mu.Unlock()
}

func (b *sseBroker) removeClient(ch chan []byte) {
	b.mu.Lock()
	delete(b.clients, ch)
	b.mu.Unlock()
	close(ch)
}

func (b *sseBroker) broadcast(msg []byte) {
	b.mu.Lock()
	for ch := range b.clients {
		select {
		case ch <- msg:
		default:
		}
	}
	b.mu.Unlock()
}

// -------------------------------------------------------------
// MAIN
// -------------------------------------------------------------
func main() {
	port := ":5055"
	fmt.Printf("⚡ Orchestrator running on http://localhost%s\n", port)

	mux := http.NewServeMux()

	// Crear carpetas
	os.MkdirAll("uploads", 0755)
	os.MkdirAll("results", 0755)

	broker := newSSEBroker()

	// --- GET /api/events ---
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		ch := make(chan []byte, 10)
		broker.addClient(ch)
		defer broker.removeClient(ch)

		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		notify := w.(http.CloseNotifier).CloseNotify()
		flusher := w.(http.Flusher)

		for {
			select {
			case msg := <-ch:
				fmt.Fprintf(w, "data: %s\n\n", msg)
				flusher.Flush()
			case <-ticker.C:
				fmt.Fprintf(w, ": ping\n\n")
				flusher.Flush()
			case <-notify:
				return
			}
		}
	})

	// --- POST /api/run ---
	mux.HandleFunc("/api/run", func(w http.ResponseWriter, r *http.Request) {
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "missing file", http.StatusBadRequest)
			return
		}
		defer file.Close()

		filename := fmt.Sprintf("upload_%d_%s", time.Now().Unix(), header.Filename)
		savePath := filepath.Join("uploads", filename)
		out, _ := os.Create(savePath)
		defer out.Close()
		io.Copy(out, file)

		start := time.Now()
		events := make(chan engine.Event, 100)

		// Broadcast de eventos del engine
		go func() {
			for ev := range events {
				data, _ := json.Marshal(ev)
				broker.broadcast(data)
			}
		}()

		// Ejecutar test
		go func() {
			engine.RunWithEvents(savePath, events)
			close(events)

			end := time.Now()
			summary := map[string]interface{}{
				"started_at": start,
				"ended_at":   end,
				"yaml_file":  filename,
			}
			outPath := fmt.Sprintf("results/run_%s.summary.json", end.Format("2006-01-02_150405"))
			os.WriteFile(outPath, mustJSON(summary), 0644)
		}()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"message": "Test started (streaming metrics via /api/events)",
		})
	})

	// --- Servir la UI ---
	uiDir := "./orchestrator_ui"
	fmt.Printf("📂 Serving UI from: %s\n", uiDir)
	mux.Handle("/", http.FileServer(http.Dir(uiDir)))

	http.ListenAndServe(port, mux)
}

// Helper
func mustJSON(v interface{}) []byte {
	data, _ := json.MarshalIndent(v, "", "  ")
	return data
}
