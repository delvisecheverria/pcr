package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"text/template"
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

		go func() {
			for ev := range events {
				data, _ := json.Marshal(ev)
				broker.broadcast(data)
			}
		}()

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

	// --- POST /api/report ---
	// Nodos remotos (GitHub Actions, etc.) envían aquí eventos JSON (engine.Event)
	// para alimentar la UI en tiempo real y visualizar métricas distribuidas.
	mux.HandleFunc("/api/report", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var ev engine.Event
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			http.Error(w, "invalid event body", http.StatusBadRequest)
			fmt.Println("❌ Invalid report received:", err)
			return
		}

		// Log descriptivo
		fmt.Printf("📩 Report received from node: %s | path: %s | status: %d | latency: %.2fms | error: %s\n",
			ev.Name, ev.Path, ev.Status, ev.LatencyMs, ev.Err)

		// Serializa y retransmite a la UI vía SSE
		data, _ := json.Marshal(ev)
		broker.broadcast(data)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"message": "Report received successfully",
		})
	})

	// --- POST /api/run-distributed ---
	mux.HandleFunc("/api/run-distributed", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		nodes := r.FormValue("nodes")
		if nodes == "" {
			nodes = "2"
		}

		// 1️⃣ Guardar YAML subido
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "missing file", http.StatusBadRequest)
			return
		}
		defer file.Close()

		outPath := "uploads/totest.yaml"
		out, _ := os.Create(outPath)
		defer out.Close()
		io.Copy(out, file)

		// 2️⃣ Template dinámico del workflow (escapando ${{ matrix.node }})
		const tpl = `name: 🌩️ Distributed Pulse Test

on:
  workflow_dispatch:

jobs:
  run-nodes:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        node: [{{range $i, $v := .}}{{if $i}}, {{end}}{{$v}}{{end}}]
    steps:
      - name: Checkout repo
        uses: actions/checkout@v4
      - name: Setup Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      - name: Build and run node
        env:
          REPORT_URL: ${{"{{"}} secrets.REPORT_URL {{"}}"}}
        run: |
          echo "🏁 Starting node ${{"{{"}} matrix.node {{"}}"}} of {{len .}}"
          go run ./cmd/worker/main.go -yaml "uploads/totest.yaml" -node ${{"{{"}} matrix.node {{"}}"}} -total {{len .}}
`

		// 3️⃣ Crear workflow file dinámico
		filePath := ".github/workflows/distributed-node.yml"
		os.MkdirAll(".github/workflows", 0755)
		f, _ := os.Create(filePath)
		defer f.Close()

		var nodeList []int
		var n int
		fmt.Sscanf(nodes, "%d", &n)
		if n <= 0 {
			n = 2
		}
		for i := 1; i <= n; i++ {
			nodeList = append(nodeList, i)
		}

		t := template.Must(template.New("workflow").Parse(tpl))
		_ = t.Execute(f, nodeList)

		// 4️⃣ Git commit & push
		cmds := [][]string{
			{"git", "add", filePath, outPath},
			{"git", "commit", "-m", fmt.Sprintf("🚀 Run distributed test with %d nodes", n)},
			{"git", "push"},
		}
		for _, args := range cmds {
			cmd := exec.Command(args[0], args[1:]...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				json.NewEncoder(w).Encode(map[string]string{
					"error": fmt.Sprintf("git error: %v (%s)", err, string(out)),
				})
				return
			}
		}

		json.NewEncoder(w).Encode(map[string]string{
			"message": fmt.Sprintf("✅ Workflow committed for %d nodes! Trigger it in Actions.", n),
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
