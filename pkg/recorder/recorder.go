package recorder

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// -------------------------------------------------------------
// RecordedEvent — enviado al frontend (UI) vía SSE
// -------------------------------------------------------------
type RecordedEvent struct {
	Method   string            `json:"method"`
	URL      string            `json:"url"`
	Status   int               `json:"status"`
	Time     string            `json:"time"`
	Headers  map[string]string `json:"headers,omitempty"`
	Body     string            `json:"body,omitempty"`
	Response string            `json:"response,omitempty"` // solo para la UI
	Proto    string            `json:"proto,omitempty"`
	Host     string            `json:"host,omitempty"`
	Port     string            `json:"port,omitempty"`
	Note     string            `json:"note,omitempty"`
}

// -------------------------------------------------------------
// Recorder — proxy principal que intercepta y graba requests
// -------------------------------------------------------------
type Recorder struct {
	Addr    string
	OutDir  string
	mu      sync.Mutex
	records []RecordedRequest // viene de model.go
	server  *http.Server
	stopCh  chan struct{}
	Events  chan RecordedEvent
}

// New crea una nueva instancia del Recorder
func New(addr, outDir string) *Recorder {
	return &Recorder{
		Addr:    addr,
		OutDir:  outDir,
		records: make([]RecordedRequest, 0, 256),
		stopCh:  make(chan struct{}),
		Events:  make(chan RecordedEvent, 100),
	}
}

// -------------------------------------------------------------
// Start — inicia el proxy recorder
// -------------------------------------------------------------
func (r *Recorder) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", r.handleHTTP)

	r.server = &http.Server{Addr: r.Addr, Handler: mux}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("[RECORDER] ▶️ Listening on %s — set your proxy to %s\n", r.Addr, r.Addr)
		if err := r.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[RECORDER] ❌ Server error: %v", err)
		}
	}()

	select {
	case <-stop:
		log.Println("[RECORDER] ⏹ Interrupted — writing YAML output...")
	case <-r.stopCh:
		log.Println("[RECORDER] 🧩 Stop signal received — writing YAML output...")
	}

	// Asegura que todo el tráfico pendiente se termine antes de guardar
	time.Sleep(2 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = r.server.Shutdown(ctx)
	close(r.Events)

	if err := r.writeYAML(); err != nil {
		return fmt.Errorf("failed to write YAML: %w", err)
	}

	log.Printf("[RECORDER] ✅ Session saved to: %s\n", r.OutDir)
	return nil
}

// Stop detiene el recorder
func (r *Recorder) Stop() {
	select {
	case <-r.stopCh:
	default:
		close(r.stopCh)
	}
}

// -------------------------------------------------------------
// handleHTTP — maneja peticiones normales (no CONNECT)
// -------------------------------------------------------------
func (r *Recorder) handleHTTP(w http.ResponseWriter, req *http.Request) {
	if strings.ToUpper(req.Method) == "CONNECT" {
		r.handleConnect(w, req)
		return
	}

	var bodyBuf []byte
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		bodyBuf = b
		req.Body = io.NopCloser(bytes.NewReader(b))
	}

	headers := make(map[string]string)
	for k, v := range req.Header {
		headers[k] = strings.Join(v, "; ")
	}
	cookies := make(map[string]string)
	for _, c := range req.Cookies() {
		cookies[c.Name] = c.Value
	}

	rec := RecordedRequest{
		Timestamp: time.Now(),
		Method:    req.Method,
		URL:       req.URL.String(),
		Host:      req.Host,
		Headers:   headers,
		Cookies:   cookies,
		Body:      string(bodyBuf),
		Proto:     req.Proto,
	}

	r.mu.Lock()
	r.records = append(r.records, rec)
	r.mu.Unlock()

	log.Printf("[RECORDER] ➡️ %s %s", req.Method, req.URL.String())

	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		http.Error(w, "Upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(bytes.NewReader(respBody))

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, bytes.NewReader(respBody))

	r.emitEvent(RecordedEvent{
		Method:   req.Method,
		URL:      req.URL.String(),
		Status:   resp.StatusCode,
		Time:     time.Now().Format("15:04:05"),
		Headers:  headers,
		Body:     string(bodyBuf),
		Response: string(respBody),
		Proto:    req.Proto,
		Host:     req.Host,
		Port:     extractPort(req.Host),
	})
}

// -------------------------------------------------------------
// handleConnect — maneja túneles HTTPS (CONNECT)
// -------------------------------------------------------------
func (r *Recorder) handleConnect(w http.ResponseWriter, req *http.Request) {
	host := req.URL.Host
	rec := RecordedRequest{
		Timestamp: time.Now(),
		Method:    "CONNECT",
		URL:       req.URL.String(),
		Host:      host,
		Note:      "HTTPS tunnel (CONNECT) - body not visible unless MITM enabled",
		Proto:     req.Proto,
	}

	r.mu.Lock()
	r.records = append(r.records, rec)
	r.mu.Unlock()

	r.emitEvent(RecordedEvent{
		Method: "CONNECT",
		URL:    req.URL.String(),
		Time:   time.Now().Format("15:04:05"),
		Note:   "HTTPS tunnel established",
	})

	hj, _ := w.(http.Hijacker)
	clientConn, _, _ := hj.Hijack()
	serverConn, err := net.DialTimeout("tcp", host, 10*time.Second)
	if err != nil {
		clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		clientConn.Close()
		return
	}
	clientConn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	go transfer(serverConn, clientConn)
	go transfer(clientConn, serverConn)
}

// -------------------------------------------------------------
// Helpers
// -------------------------------------------------------------
func transfer(dst net.Conn, src net.Conn) {
	defer dst.Close()
	defer src.Close()
	io.Copy(dst, src)
}

func (r *Recorder) emitEvent(ev RecordedEvent) {
	select {
	case r.Events <- ev:
	default:
	}
}

func extractPort(host string) string {
	if strings.Contains(host, ":") {
		return strings.Split(host, ":")[1]
	}
	return "80"
}

// -------------------------------------------------------------
// writeYAML — genera archivo Pulse YAML limpio y ordenado
// -------------------------------------------------------------
func (r *Recorder) writeYAML() error {
	r.mu.Lock()
	records := make([]RecordedRequest, len(r.records))
	copy(records, r.records)
	r.mu.Unlock()

	if len(records) == 0 {
		log.Println("[RECORDER] ⚠️ No requests recorded — skipping YAML generation.")
		return nil
	}

	out := map[string]interface{}{
		"version":     "1.0",
		"scenario":    "Recorded Session",
		"concurrency": 1,
		"duration":    "10s",
	}

	var requests []map[string]interface{}

	for i, rec := range records {
		protocol := "https"
		path := rec.URL
		if strings.HasPrefix(rec.URL, "http://") {
			protocol = "http"
			path = strings.TrimPrefix(rec.URL, "http://"+rec.Host)
		} else if strings.HasPrefix(rec.URL, "https://") {
			path = strings.TrimPrefix(rec.URL, "https://"+rec.Host)
		}

		cleanHeaders := map[string]string{}
		for k, v := range rec.Headers {
			if strings.ToLower(k) != "cookie" {
				cleanHeaders[k] = v
			}
		}

		req := map[string]interface{}{
			"name":     fmt.Sprintf("%02d_%s", i+1, sanitizeName(rec.Method+"_"+rec.Host)),
			"method":   rec.Method,
			"protocol": protocol,
			"host":     rec.Host,
			"path":     path,
			"headers":  cleanHeaders,
			"body":     rec.Body, // ✅ corregido: ya no usa yaml.Node
		}

		if rec.Note != "" {
			req["note"] = rec.Note
		}

		requests = append(requests, req)
	}

	out["requests"] = requests

	if r.OutDir == "" {
		r.OutDir = "."
	}
	_ = os.MkdirAll(r.OutDir, 0755)
	filename := filepath.Join(r.OutDir, fmt.Sprintf("recorded_%s.pulse.yaml", time.Now().Format("2006-01-02_150405")))
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := yaml.NewEncoder(f)
	enc.SetIndent(2)
	return enc.Encode(out)
}

func sanitizeName(s string) string {
	s = strings.ReplaceAll(s, ":", "_")
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, " ", "_")
	return s
}
