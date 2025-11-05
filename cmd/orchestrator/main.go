package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	// --- POST /api/report ---
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

		fmt.Printf("📩 Report received from node: %s | path: %s | status: %d | latency: %.2fms | error: %s\n",
			ev.Name, ev.Path, ev.Status, ev.LatencyMs, ev.Err)

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

		const tpl = `name: 🌩️ Distributed Pulse Test

on:
  workflow_dispatch:
    inputs:
      nodes:
        description: "Number of nodes to launch"
        required: true
        default: "2"

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

		// 3️⃣ Commit + Push
		timestamp := time.Now().Format("20060102_150405")
		cmds := [][]string{
			{"git", "add", filePath, outPath},
			{"git", "commit", "--allow-empty", "-m", fmt.Sprintf("🚀 Run distributed test (%s) with %d nodes", timestamp, n)},
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

		// 4️⃣ Disparar el workflow por API
		token := os.Getenv("PERSONAL_GITHUB_TOKEN")
		if token == "" {
			fmt.Println("⚠️ No PERSONAL_GITHUB_TOKEN set — skipping dispatch trigger")
		} else {
			dispatchURL := "https://api.github.com/repos/delvisecheverria/pcr/actions/workflows/distributed-node.yml/dispatches"
			payload := strings.NewReader(fmt.Sprintf(`{"ref":"main","inputs":{"nodes":"%d"}}`, n))
			req, _ := http.NewRequest("POST", dispatchURL, payload)
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Accept", "application/vnd.github+json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				fmt.Println("❌ Error triggering workflow:", err)
			} else {
				fmt.Println("🚀 Workflow dispatch status:", resp.Status)
			}
		}

		json.NewEncoder(w).Encode(map[string]string{
			"message": fmt.Sprintf("✅ Workflow committed and dispatched for %d nodes!", n),
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
