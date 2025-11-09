package engine

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
)

// -------------------------------------------------------------
// Estructuras base del escenario (nuevo formato con scenarios)
// -------------------------------------------------------------

type Request struct {
	Name     string            `yaml:"name"`
	Method   string            `yaml:"method"`
	Protocol string            `yaml:"protocol"`
	Host     string            `yaml:"host"`
	Path     string            `yaml:"path"`
	Headers  map[string]string `yaml:"headers"`
	Body     string            `yaml:"body,omitempty"`
}

type Profile struct {
	Concurrency   int    `yaml:"concurrency"`
	RampUp        string `yaml:"ramp_up"`
	Duration      string `yaml:"duration"`
	RampDown      string `yaml:"ramp_down"`
	Iterations    int    `yaml:"iterations"`
	StartupDelay  string `yaml:"startup_delay"`
}

type Scenario struct {
	Name     string    `yaml:"name"`
	Profile  Profile   `yaml:"profile"`
	Requests []Request `yaml:"requests"`
}

type ScenarioFile struct {
	Scenarios []Scenario `yaml:"scenarios"`
}

// -------------------------------------------------------------
// Eventos en vivo (para SSE / WebSocket)
// -------------------------------------------------------------

type Event struct {
	Timestamp   time.Time `json:"ts"`
	Name        string    `json:"name"`
	Method      string    `json:"method"`
	Path        string    `json:"path"`
	Status      int       `json:"status"`
	LatencyMs   float64   `json:"latency_ms"`
	Err         string    `json:"err,omitempty"`
	Concurrency int       `json:"concurrency"`
}

// -------------------------------------------------------------
// Tipos internos para métricas agregadas
// -------------------------------------------------------------

type requestStat struct {
	name      string
	latencies []time.Duration
	failures  int
}

// -------------------------------------------------------------
// API pública
// -------------------------------------------------------------

// Run: versión clásica (sin eventos)
func Run(path string) error {
	return runInternal(path, nil)
}

// RunWithEvents: igual que Run pero emite un Event por request completado
func RunWithEvents(path string, events chan<- Event) error {
	return runInternal(path, events)
}

// -------------------------------------------------------------
// Implementación principal
// -------------------------------------------------------------

func runInternal(path string, events chan<- Event) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("cannot read YAML file: %v", err)
	}

	var file ScenarioFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("invalid YAML format: %v", err)
	}

	if len(file.Scenarios) == 0 {
		return fmt.Errorf("no scenarios found in YAML")
	}

	scenario := file.Scenarios[0]
	profile := scenario.Profile

	fmt.Printf("🚀 Running scenario: %s\n", scenario.Name)
	fmt.Printf("Concurrency: %d | Duration: %s | Ramp-up: %s\n",
		profile.Concurrency, profile.Duration, profile.RampUp)

	duration, err := time.ParseDuration(profile.Duration)
	if err != nil {
		return fmt.Errorf("invalid duration: %v", err)
	}

	rampUp := time.Duration(0)
	if profile.RampUp != "" {
		if ru, err := time.ParseDuration(profile.RampUp); err == nil {
			rampUp = ru
		}
	}

	start := time.Now()
	stats := make(map[string]*requestStat)

	type result struct {
		name    string
		method  string
		path    string
		status  int
		latency time.Duration
		err     error
	}
	results := make(chan result, 10000)

	// Cálculo del escalón entre workers para el ramp-up
	var step time.Duration
	if rampUp > 0 && profile.Concurrency > 0 {
		step = rampUp / time.Duration(profile.Concurrency)
	}

	var wg sync.WaitGroup
	var activeUsers int32 = 0

	for i := 0; i < profile.Concurrency; i++ {
		wg.Add(1)
		go func(workerIdx int) {
			defer wg.Done()

			// Ramp-up escalonado
			if step > 0 {
				time.Sleep(step * time.Duration(workerIdx))
			}

			cur := atomic.AddInt32(&activeUsers, 1)
			if events != nil {
				events <- Event{
					Timestamp:   time.Now(),
					Name:        "RAMP_PROGRESS",
					Method:      "SYSTEM",
					Path:        fmt.Sprintf("Worker #%d started", workerIdx+1),
					Concurrency: int(cur),
				}
			}

			client := &http.Client{Timeout: 15 * time.Second}

			for time.Since(start) < duration {
				for _, reqCfg := range scenario.Requests {
					url := fmt.Sprintf("%s://%s%s", reqCfg.Protocol, reqCfg.Host, reqCfg.Path)

					var body io.Reader
					if reqCfg.Body != "" {
						body = bytes.NewBuffer([]byte(reqCfg.Body))
					}

					req, err := http.NewRequest(reqCfg.Method, url, body)
					if err != nil {
						results <- result{
							name:   fmt.Sprintf("%s %s", reqCfg.Method, reqCfg.Path),
							method: reqCfg.Method,
							path:   reqCfg.Path,
							status: 0,
							err:    err,
						}
						continue
					}

					for k, v := range reqCfg.Headers {
						req.Header.Set(k, v)
					}

					t0 := time.Now()
					resp, err := client.Do(req)
					latency := time.Since(t0)

					if err != nil {
						results <- result{
							name:    fmt.Sprintf("%s %s", reqCfg.Method, reqCfg.Path),
							method:  reqCfg.Method,
							path:    reqCfg.Path,
							status:  0,
							latency: latency,
							err:     err,
						}
						continue
					}

					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()

					if resp.StatusCode >= 400 {
						results <- result{
							name:    fmt.Sprintf("%s %s", reqCfg.Method, reqCfg.Path),
							method:  reqCfg.Method,
							path:    reqCfg.Path,
							status:  resp.StatusCode,
							latency: latency,
							err:     fmt.Errorf("status %d", resp.StatusCode),
						}
					} else {
						results <- result{
							name:    fmt.Sprintf("%s %s", reqCfg.Method, reqCfg.Path),
							method:  reqCfg.Method,
							path:    reqCfg.Path,
							status:  resp.StatusCode,
							latency: latency,
						}
					}
				}
			}
		}(i)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	// Consumo de resultados
	for r := range results {
		curConc := int(atomic.LoadInt32(&activeUsers))
		if events != nil {
			ev := Event{
				Timestamp:   time.Now(),
				Name:        r.name,
				Method:      r.method,
				Path:        r.path,
				Status:      r.status,
				LatencyMs:   float64(r.latency.Microseconds()) / 1000.0,
				Concurrency: curConc,
			}
			if r.err != nil {
				ev.Err = r.err.Error()
			}
			select {
			case events <- ev:
			default:
			}
		}

		stat, ok := stats[r.name]
		if !ok {
			stat = &requestStat{name: r.name}
			stats[r.name] = stat
		}
		stat.latencies = append(stat.latencies, r.latency)
		if r.err != nil {
			stat.failures++
		}
	}

	return summarize(stats)
}

// -------------------------------------------------------------
// Resumen e impresión
// -------------------------------------------------------------

func summarize(stats map[string]*requestStat) error {
	if len(stats) == 0 {
		fmt.Println("No requests executed.")
		return nil
	}

	var globalLatencies []time.Duration
	var totalFails int
	var totalCount int

	fmt.Println("\n--- PER REQUEST METRICS ---")
	fmt.Printf("%-30s %-10s %-10s %-10s %-10s %-10s %-10s\n",
		"Request", "Count", "Fails", "Err(%)", "Avg(ms)", "P90(ms)", "P95(ms)")

	names := make([]string, 0, len(stats))
	for k := range stats {
		names = append(names, k)
	}
	sort.Strings(names)

	for _, name := range names {
		s := stats[name]
		if len(s.latencies) == 0 {
			continue
		}
		sort.Slice(s.latencies, func(i, j int) bool { return s.latencies[i] < s.latencies[j] })

		count := len(s.latencies)
		totalFails += s.failures
		totalCount += count

		avg := avgDuration(s.latencies)
		p90 := percentile(s.latencies, 90)
		p95 := percentile(s.latencies, 95)
		errorRate := (float64(s.failures) / float64(count)) * 100

		fmt.Printf("%-30s %-10d %-10d %-10.2f %-10.2f %-10.2f %-10.2f\n",
			s.name, count, s.failures, errorRate, ms(avg), ms(p90), ms(p95))

		globalLatencies = append(globalLatencies, s.latencies...)
	}

	sort.Slice(globalLatencies, func(i, j int) bool { return globalLatencies[i] < globalLatencies[j] })
	avgGlobal := avgDuration(globalLatencies)
	p95Global := percentile(globalLatencies, 95)

	fmt.Println("\n--- RESULTS ---")
	fmt.Printf("Total Requests: %d\n", totalCount)
	fmt.Printf("Failures: %d\n", totalFails)
	fmt.Printf("Average Latency: %.2fms\n", ms(avgGlobal))
	fmt.Printf("P95 Latency: %.2fms\n", ms(p95Global))
	fmt.Println("----------------")

	return nil
}

// -------------------------------------------------------------
// Helpers
// -------------------------------------------------------------

func avgDuration(durations []time.Duration) time.Duration {
	var sum time.Duration
	for _, d := range durations {
		sum += d
	}
	if len(durations) == 0 {
		return 0
	}
	return sum / time.Duration(len(durations))
}

func percentile(durations []time.Duration, p int) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	k := int(float64(len(durations)) * float64(p) / 100.0)
	if k >= len(durations) {
		k = len(durations) - 1
	}
	return durations[k]
}

func ms(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000.0
}
