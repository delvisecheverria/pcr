package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"pulse/pkg/engine"
)

func main() {
	yamlPath := flag.String("yaml", "uploads/totest.yaml", "Path del YAML a ejecutar")
	nodeID := flag.Int("node", 1, "ID del nodo actual")
	totalNodes := flag.Int("total", 1, "Cantidad total de nodos")
	flag.Parse()

	reportURL := os.Getenv("REPORT_URL") // p.ej. https://<tu-orchestrator>/api/report
	if reportURL == "" {
		log.Println("⚠️ REPORT_URL no seteado: eventos NO se reportarán al orquestador.")
	}

	fmt.Printf("⚙️ Node %d/%d executing scenario: %s\n", *nodeID, *totalNodes, *yamlPath)

	events := make(chan engine.Event, 100)
	go func() {
		for ev := range events {
			// añade metadata del nodo al nombre del evento (opcional)
			ev.Name = fmt.Sprintf("[node-%d] %s", *nodeID, ev.Name)
			if reportURL != "" {
				b, _ := json.Marshal(ev)
				_, _ = http.Post(reportURL, "application/json", bytes.NewReader(b))
			} else {
				// fallback: imprime a stdout
				log.Printf("%s %s -> %d (%.1fms)", ev.Method, ev.Path, ev.Status, ev.LatencyMs)
			}
		}
	}()

	// Ejecutar escenario con métricas en vivo
	if err := engine.RunWithEvents(*yamlPath, events); err != nil {
		log.Fatalf("❌ Node %d failed: %v", *nodeID, err)
	}
	close(events)

	fmt.Printf("✅ Node %d finished successfully!\n", *nodeID)

	// --- Reporte final al orquestador (si REPORT_URL está definido) ---
	if reportURL != "" {
		summary := map[string]interface{}{
			"node_id":     *nodeID,
			"total_nodes": *totalNodes,
			"status":      "success",
			"timestamp":   time.Now().Format(time.RFC3339),
			"summary": map[string]interface{}{
				"requests": 0,        // puedes ajustar si guardas métricas
				"failures": 0,
				"avg_ms":   0.0,
				"p95_ms":   0.0,
			},
		}
		payload, _ := json.Marshal(summary)
		resp, err := http.Post(reportURL, "application/json", bytes.NewBuffer(payload))
		if err != nil {
			log.Printf("⚠️ Node %d could not send summary report: %v\n", *nodeID, err)
		} else {
			defer resp.Body.Close()
			log.Printf("📤 Node %d summary report sent (%s)\n", *nodeID, resp.Status)
		}
	} else {
		log.Printf("⚠️ Node %d: REPORT_URL not set, skipping summary report\n", *nodeID)
	}
}
