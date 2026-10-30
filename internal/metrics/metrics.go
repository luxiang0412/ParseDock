// Package metrics is a small, dependency-free Prometheus exposition.
//
// ParseDock deliberately avoids a metrics SDK: the surface is a handful of counters,
// gauges and histograms, the text format is a stable contract, and a client library
// would add a dependency we would have to audit for the licence gate. Everything here
// is safe for concurrent use.
package metrics

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Default is the process-wide registry used by the coordinator.
var Default = New()

// Registry owns all instruments of one process.
type Registry struct {
	mu         sync.RWMutex
	order      []string
	counters   map[string]*CounterVec
	gauges     map[string]*GaugeVec
	histograms map[string]*HistogramVec
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{
		counters:   map[string]*CounterVec{},
		gauges:     map[string]*GaugeVec{},
		histograms: map[string]*HistogramVec{},
	}
}

// CounterVec is a counter family with a fixed label set.
type CounterVec struct {
	name, help string
	labels     []string
	mu         sync.RWMutex
	series     map[string]*uint64
	labelVals  map[string][]string
}

// GaugeVec is a gauge family with a fixed label set.
type GaugeVec struct {
	name, help string
	labels     []string
	mu         sync.RWMutex
	series     map[string]*uint64 // float64 bits
	labelVals  map[string][]string
}

// HistogramVec is a histogram family with default latency buckets (seconds).
type HistogramVec struct {
	name, help string
	labels     []string
	bounds     []float64
	mu         sync.RWMutex
	series     map[string]*histSeries
	labelVals  map[string][]string
}

type histSeries struct {
	counts []uint64
	sum    float64
	count  uint64
}

var defaultBounds = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300}

// Counter registers (or returns) a counter family.
func (r *Registry) Counter(name, help string, labels ...string) *CounterVec {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.counters[name]; ok {
		return c
	}
	c := &CounterVec{name: name, help: help, labels: labels,
		series: map[string]*uint64{}, labelVals: map[string][]string{}}
	r.counters[name] = c
	r.order = append(r.order, name)
	return c
}

// Gauge registers (or returns) a gauge family.
func (r *Registry) Gauge(name, help string, labels ...string) *GaugeVec {
	r.mu.Lock()
	defer r.mu.Unlock()
	if g, ok := r.gauges[name]; ok {
		return g
	}
	g := &GaugeVec{name: name, help: help, labels: labels,
		series: map[string]*uint64{}, labelVals: map[string][]string{}}
	r.gauges[name] = g
	r.order = append(r.order, name)
	return g
}

// Histogram registers (or returns) a histogram family.
func (r *Registry) Histogram(name, help string, labels ...string) *HistogramVec {
	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := r.histograms[name]; ok {
		return h
	}
	h := &HistogramVec{name: name, help: help, labels: labels, bounds: defaultBounds,
		series: map[string]*histSeries{}, labelVals: map[string][]string{}}
	r.histograms[name] = h
	r.order = append(r.order, name)
	return h
}

func labelKey(values []string) string { return strings.Join(values, "\x00") }

// With returns the series for these label values, creating it on first use.
func (c *CounterVec) With(values ...string) *Counter {
	key := seriesKey(c.name, c.labels, values)
	c.mu.RLock()
	p, ok := c.series[key]
	c.mu.RUnlock()
	if !ok {
		c.mu.Lock()
		if p, ok = c.series[key]; !ok {
			p = new(uint64)
			c.series[key] = p
			c.labelVals[key] = values
		}
		c.mu.Unlock()
	}
	return &Counter{p: p}
}

// With returns the series for these label values, creating it on first use.
func (g *GaugeVec) With(values ...string) *Gauge {
	key := seriesKey(g.name, g.labels, values)
	g.mu.RLock()
	p, ok := g.series[key]
	g.mu.RUnlock()
	if !ok {
		g.mu.Lock()
		if p, ok = g.series[key]; !ok {
			p = new(uint64)
			g.series[key] = p
			g.labelVals[key] = values
		}
		g.mu.Unlock()
	}
	return &Gauge{p: p}
}

// With returns the histogram series for these label values.
func (h *HistogramVec) With(values ...string) *Histogram {
	key := seriesKey(h.name, h.labels, values)
	h.mu.RLock()
	s, ok := h.series[key]
	h.mu.RUnlock()
	if !ok {
		h.mu.Lock()
		if s, ok = h.series[key]; !ok {
			s = &histSeries{counts: make([]uint64, len(h.bounds))}
			h.series[key] = s
			h.labelVals[key] = values
		}
		h.mu.Unlock()
	}
	return &Histogram{bounds: h.bounds, s: s}
}

func seriesKey(name string, labels, values []string) string {
	if len(labels) != len(values) {
		panic(fmt.Sprintf("metrics: %s wants %d labels, got %d", name, len(labels), len(values)))
	}
	return labelKey(values)
}

// Counter is a handle to one counter series.
type Counter struct{ p *uint64 }

// Inc increments the counter by one.
func (c *Counter) Inc() { atomic.AddUint64(c.p, 1) }

// Add increments the counter by n.
func (c *Counter) Add(n uint64) { atomic.AddUint64(c.p, n) }

// Gauge is a handle to one gauge series. The value is stored as float64 bits so a
// gauge can be updated atomically without a lock.
type Gauge struct{ p *uint64 }

// Set stores a gauge value.
func (g *Gauge) Set(v float64) { atomic.StoreUint64(g.p, math.Float64bits(v)) }

// Add adds delta to the gauge.
func (g *Gauge) Add(delta float64) {
	for {
		old := atomic.LoadUint64(g.p)
		nv := math.Float64frombits(old) + delta
		if atomic.CompareAndSwapUint64(g.p, old, math.Float64bits(nv)) {
			return
		}
	}
}

// Histogram is a handle to one histogram series.
type Histogram struct {
	bounds []float64
	mu     sync.Mutex
	s      *histSeries
}

// Observe records one value.
func (h *Histogram) Observe(v float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, b := range h.bounds {
		if v <= b {
			h.s.counts[i]++
		}
	}
	h.s.sum += v
	h.s.count++
}

// WriteText renders every instrument in the Prometheus text format.
func (r *Registry) WriteText(w io.Writer) error {
	r.mu.RLock()
	order := append([]string(nil), r.order...)
	counters := make(map[string]*CounterVec, len(r.counters))
	for k, v := range r.counters {
		counters[k] = v
	}
	gauges := make(map[string]*GaugeVec, len(r.gauges))
	for k, v := range r.gauges {
		gauges[k] = v
	}
	hists := make(map[string]*HistogramVec, len(r.histograms))
	for k, v := range r.histograms {
		hists[k] = v
	}
	r.mu.RUnlock()

	sort.Strings(order)
	for _, name := range order {
		var err error
		switch {
		case counters[name] != nil:
			err = counters[name].write(w)
		case gauges[name] != nil:
			err = gauges[name].write(w)
		case hists[name] != nil:
			err = hists[name].write(w)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func fmtLabels(labels, values []string, extra ...string) string {
	if len(labels) == 0 && len(extra) == 0 {
		return ""
	}
	parts := make([]string, 0, len(labels)+len(extra)/2)
	for i, l := range labels {
		parts = append(parts, l+"=\""+escape(values[i])+"\"")
	}
	for i := 0; i+1 < len(extra); i += 2 {
		parts = append(parts, extra[i]+"=\""+escape(extra[i+1])+"\"")
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func escape(s string) string {
	return strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n").Replace(s)
}

func (c *CounterVec) write(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", c.name, c.help, c.name); err != nil {
		return err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	keys := sortedKeys(c.labelVals)
	for _, k := range keys {
		v := atomic.LoadUint64(c.series[k])
		if _, err := fmt.Fprintf(w, "%s%s %d\n", c.name, fmtLabels(c.labels, c.labelVals[k]), v); err != nil {
			return err
		}
	}
	return nil
}

func (g *GaugeVec) write(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n", g.name, g.help, g.name); err != nil {
		return err
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, k := range sortedKeys(g.labelVals) {
		v := math.Float64frombits(atomic.LoadUint64(g.series[k]))
		if _, err := fmt.Fprintf(w, "%s%s %s\n", g.name, fmtLabels(g.labels, g.labelVals[k]), formatFloat(v)); err != nil {
			return err
		}
	}
	return nil
}

func (h *HistogramVec) write(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s histogram\n", h.name, h.help, h.name); err != nil {
		return err
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, k := range sortedKeys(h.labelVals) {
		s := h.series[k]
		base := h.labelVals[k]
		for i, b := range h.bounds {
			if _, err := fmt.Fprintf(w, "%s_bucket%s %d\n", h.name,
				fmtLabels(h.labels, base, "le", formatFloat(b)), s.counts[i]); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "%s_bucket%s %d\n", h.name,
			fmtLabels(h.labels, base, "le", "+Inf"), s.count); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "%s_sum%s %s\n", h.name, fmtLabels(h.labels, base), formatFloat(s.sum)); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "%s_count%s %d\n", h.name, fmtLabels(h.labels, base), s.count); err != nil {
			return err
		}
	}
	return nil
}

func formatFloat(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
