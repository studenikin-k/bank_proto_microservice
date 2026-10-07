package utils

import (
	"encoding/json"
	"expvar"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"sync"
	"time"
)

// Метрики процесса без внешних систем: стандартные expvar и pprof.
//
//	GET  <DEBUG_ADDR>/debug/vars           — счётчики и гистограммы латентности (JSON)
//	POST <DEBUG_ADDR>/debug/metrics/reset  — обнулить гистограммы перед замером
//	GET  <DEBUG_ADDR>/debug/pprof/         — профилировщик CPU/памяти
//
// Гистограммы каждого сервиса позволяют разложить время ответа по хопам:
// шлюз (весь запрос) -> Transaction Service -> Account Service.

// Outcome — итог обработки запроса для метрик.
type Outcome int

const (
	OutcomeOK       Outcome = iota
	OutcomeRejected         // определённый отказ по бизнес-правилу или валидации (4xx)
	OutcomeError            // сбой: таймаут, недоступность, внутренняя ошибка (5xx)
)

// Верхние границы корзин гистограммы, мс.
var bucketBounds = []float64{1, 2, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000}

type Histogram struct {
	mu       sync.Mutex
	count    int64
	rejected int64
	errors   int64
	sum      time.Duration
	max      time.Duration
	buckets  []int64 // последняя корзина — всё, что больше последней границы
}

func newHistogram() *Histogram {
	return &Histogram{buckets: make([]int64, len(bucketBounds)+1)}
}

func (h *Histogram) Observe(d time.Duration, o Outcome) {
	ms := float64(d) / float64(time.Millisecond)
	i := 0
	for i < len(bucketBounds) && ms > bucketBounds[i] {
		i++
	}
	h.mu.Lock()
	h.count++
	h.sum += d
	if d > h.max {
		h.max = d
	}
	h.buckets[i]++
	switch o {
	case OutcomeRejected:
		h.rejected++
	case OutcomeError:
		h.errors++
	}
	h.mu.Unlock()
}

func (h *Histogram) reset() {
	h.mu.Lock()
	h.count, h.rejected, h.errors, h.sum, h.max = 0, 0, 0, 0, 0
	for i := range h.buckets {
		h.buckets[i] = 0
	}
	h.mu.Unlock()
}

// quantile оценивает квантиль сверху: граница корзины, в которой он находится.
func (h *Histogram) quantile(q float64) float64 {
	if h.count == 0 {
		return 0
	}
	target := int64(q*float64(h.count) + 0.5)
	var cum int64
	for i, c := range h.buckets {
		cum += c
		if cum >= target {
			if i < len(bucketBounds) {
				return bucketBounds[i]
			}
			break
		}
	}
	return float64(h.max) / float64(time.Millisecond)
}

// String реализует expvar.Var.
func (h *Histogram) String() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	avg := 0.0
	if h.count > 0 {
		avg = float64(h.sum) / float64(h.count) / float64(time.Millisecond)
	}
	b, _ := json.Marshal(map[string]any{
		"count":      h.count,
		"rejected":   h.rejected,
		"errors":     h.errors,
		"avg_ms":     avg,
		"max_ms":     float64(h.max) / float64(time.Millisecond),
		"p50_le_ms":  h.quantile(0.50),
		"p95_le_ms":  h.quantile(0.95),
		"p99_le_ms":  h.quantile(0.99),
		"buckets_ms": h.buckets,
		"bounds_ms":  bucketBounds,
	})
	return string(b)
}

var (
	latencyVars = expvar.NewMap("latency")
	histograms  sync.Map // name -> *Histogram
)

// Track возвращает гистограмму латентности с указанным именем (создаёт при первом обращении).
func Track(name string) *Histogram {
	if h, ok := histograms.Load(name); ok {
		return h.(*Histogram)
	}
	h, loaded := histograms.LoadOrStore(name, newHistogram())
	if !loaded {
		latencyVars.Set(name, h.(*Histogram))
	}
	return h.(*Histogram)
}

// ResetMetrics обнуляет все гистограммы.
func ResetMetrics() {
	histograms.Range(func(_, v any) bool {
		v.(*Histogram).reset()
		return true
	})
}

// StartDebugServer запускает HTTP-сервер с /debug/vars и /debug/pprof на addr.
// Пустой addr отключает сервер.
func StartDebugServer(addr string) {
	if addr == "" {
		return
	}
	mux := http.NewServeMux()
	mux.Handle("/debug/vars", expvar.Handler())
	mux.HandleFunc("/debug/metrics/reset", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "use POST", http.StatusMethodNotAllowed)
			return
		}
		ResetMetrics()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	go func() {
		slog.Info("debug-сервер (expvar, pprof) запущен", "addr", addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			slog.Error("debug-сервер остановлен", "err", err)
		}
	}()
}
