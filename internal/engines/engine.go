// Package engines defines the adapter boundary between ParseDock's control
// plane and concrete document parsers.
//
// The control plane (API/Coordinator/Worker queue) never imports an engine's
// own data types: every adapter returns ParseDock's Document IR. Engines are
// invoked as subprocesses or HTTP calls so a parser crash cannot take down the
// worker process, and so resource limits can be applied per engine.
package engines

import (
	"context"
	"fmt"
	"sort"
	"time"

	"parsedock.dev/parsedock/internal/documentir"
)

// Request is one parse invocation.
type Request struct {
	JobID     string
	Path      string // absolute path to the input copy
	Format    string // canonical format name (pdf, docx, ...)
	MimeType  string
	Mode      string // fast | accurate | auto
	Device    string // device actually used (cpu/gpu)
	WorkDir   string // scratch directory owned by the worker
	Timeout   time.Duration
	PageLimit int
	// Progress is called throttled by the worker; adapters may report coarse
	// stages. It must be safe to call from the parse goroutine.
	Progress func(percent int, stage string)
	// ReportExecution, when non-nil, receives what this adapter could prove about
	// the device that executed the parse. It must be called at most once, before
	// Parse returns, and is safe to call from the parse goroutine. Adapters that
	// cannot prove anything simply never call it; the worker then treats the
	// evidence as unknown rather than assuming the engine's declared device.
	ReportExecution func(Execution)
}

// Report is a small helper so adapters can be terse:
//
//	if req.ReportExecution != nil { req.ReportExecution(CPUEvidence(...)) }
func (r Request) Report(e Execution) {
	if r.ReportExecution != nil {
		r.ReportExecution(e)
	}
}

// Engine adapts one parser.
type Engine interface {
	Name() string
	Version() string
	Device() string // cpu | gpu | both
	Formats() []string
	Modes() []string // fast | accurate | auto
	Parse(ctx context.Context, req Request) (*documentir.Document, error)
	Probe(ctx context.Context) error
}

// Error is an engine-level failure with a stable code.
type Error struct {
	Code      string // jobs.Err* code
	Message   string
	Retryable bool
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Errf builds an engine error.
func Errf(retryable bool, code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Retryable: retryable}
}

// Registry holds the engines this worker can execute.
type Registry struct {
	engines map[string]Engine
}

// NewRegistry builds a registry.
func NewRegistry(list ...Engine) *Registry {
	r := &Registry{engines: map[string]Engine{}}
	for _, e := range list {
		r.engines[e.Name()] = e
	}
	return r
}

// Add registers one engine.
func (r *Registry) Add(e Engine) { r.engines[e.Name()] = e }

// Get looks up an engine by name.
func (r *Registry) Get(name string) (Engine, bool) {
	e, ok := r.engines[name]
	return e, ok
}

// Names returns registered engine names (registration payload).
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.engines))
	for n := range r.engines {
		out = append(out, n)
	}
	return out
}

// All returns the engines.
func (r *Registry) All() []Engine {
	out := make([]Engine, 0, len(r.engines))
	for _, e := range r.engines {
		out = append(out, e)
	}
	return out
}

// Supports reports whether any registered engine can parse format in mode.
func (r *Registry) Supports(format, mode string) bool {
	for _, e := range r.engines {
		if supportsFormat(e, format) && supportsMode(e, mode) {
			return true
		}
	}
	return false
}

func supportsFormat(e Engine, format string) bool {
	for _, f := range e.Formats() {
		if f == format || f == "*" {
			return true
		}
	}
	return false
}

// supportsMode mirrors coordinator.EngineSpec.Supports: auto requests accept
// any engine, explicit modes require an explicit declaration.
func supportsMode(e Engine, mode string) bool {
	modes := e.Modes()
	if len(modes) == 0 {
		return false
	}
	if mode == "" || mode == "auto" {
		return true
	}
	for _, m := range modes {
		if m == mode || m == "*" {
			return true
		}
	}
	return false
}

// Pick chooses the best engine for a task. The order is deterministic:
//
//  1. the coordinator's hint when it can serve the format and mode;
//  2. mode preference (fast first for mode=fast, accurate first otherwise);
//  3. exact format support over a wildcard "*";
//  4. declared priority (CapabilityFor), then name.
//
// Determinism matters: a worker must not pick a different engine for the same
// job on a retry, and two workers must behave the same way.
func (r *Registry) Pick(hint, format, mode string) (Engine, error) {
	if e, ok := r.engines[hint]; ok && supportsFormat(e, format) && supportsMode(e, mode) {
		return e, nil
	}
	modes := []string{"accurate", "fast"}
	if mode == "fast" {
		modes = []string{"fast", "accurate"}
	}
	for _, want := range modes {
		if want != mode && mode != "" && mode != "auto" {
			continue // an explicit mode never falls back to the other one
		}
		var candidates []Engine
		for _, e := range r.engines {
			if !supportsFormat(e, format) {
				continue
			}
			for _, m := range e.Modes() {
				if m == want || m == "*" {
					candidates = append(candidates, e)
					break
				}
			}
		}
		if len(candidates) == 0 {
			continue
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			ei, ej := candidates[i], candidates[j]
			exactI, exactJ := !hasWildcard(ei, format), !hasWildcard(ej, format)
			if exactI != exactJ {
				return exactI
			}
			pi, pj := priorityOf(ei), priorityOf(ej)
			if pi != pj {
				return pi > pj
			}
			return ei.Name() < ej.Name()
		})
		return candidates[0], nil
	}
	if mode == "" || mode == "auto" && r.Supports(format, mode) {
		var best Engine
		for _, e := range r.engines {
			if !supportsFormat(e, format) {
				continue
			}
			if best == nil || priorityOf(e) > priorityOf(best) ||
				(priorityOf(e) == priorityOf(best) && e.Name() < best.Name()) {
				best = e
			}
		}
		if best != nil {
			return best, nil
		}
	}
	return nil, Errf(false, "unsupported_type", "no engine for format %q (mode %q)", format, mode)
}

func hasWildcard(e Engine, format string) bool {
	for _, f := range e.Formats() {
		if f == "*" {
			return true
		}
	}
	return false
}

func priorityOf(e Engine) int {
	if c, ok := CapabilityFor(e.Name()); ok {
		return c.Priority
	}
	return 0
}
