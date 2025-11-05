package main

import (
	"flag"
	"fmt"
	"log"
	"pulse/engine" // 👈 Asegúrate de que el módulo se llama así en tu go.mod
)

// Este programa actúa como "nodo" de ejecución distribuido
// Se usa tanto localmente como desde GitHub Actions o Fly.io
func main() {
	yamlPath := flag.String("yaml", "uploads/test.yaml", "Path del YAML a ejecutar")
	nodeID := flag.Int("node", 1, "ID del nodo actual")
	totalNodes := flag.Int("total", 1, "Cantidad total de nodos")
	flag.Parse()

	fmt.Printf("⚙️ Node %d/%d executing scenario: %s\n", *nodeID, *totalNodes, *yamlPath)

	err := engine.Run(*yamlPath)
	if err != nil {
		log.Fatalf("❌ Node %d failed: %v", *nodeID, err)
	}

	fmt.Printf("✅ Node %d finished successfully!\n", *nodeID)
}

