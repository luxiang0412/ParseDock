// Package sysinfo reports host capabilities without external dependencies.
// GPU detection shells out to nvidia-smi (optional, never required).
package sysinfo

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// CPU returns usable cores.
func CPU() int { return runtime.NumCPU() }

// MemoryBytes returns total physical memory (0 if unknown).
func MemoryBytes() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
					return kb * 1024
				}
			}
		}
	}
	return 0
}

// GPU describes one detected accelerator.
type GPU struct {
	Index      int    `json:"index"`
	Name       string `json:"name"`
	MemoryMB   int64  `json:"memory_mb"`
	DriverVers string `json:"driver_version"`
	ComputeCap string `json:"compute_cap,omitempty"`
}

// MaxFreeVRAMMB returns the largest free memory of any single GPU. It is deliberately
// not a sum: ParseDock does not pool memory or select a device, so two cards with 4 GiB
// free cannot host an 8 GiB model. The boolean is false when the value is unknown (no
// nvidia-smi, unusable output, driver error), which callers must treat as "no
// information", not as "no memory".
func MaxFreeVRAMMB(ctx context.Context) (int64, bool) {
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		return 0, false
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(c, "nvidia-smi",
		"--query-gpu=memory.free", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return 0, false
	}
	var best, parsed int64
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if v, err := strconv.ParseInt(strings.TrimSpace(line), 10, 64); err == nil {
			if v > best {
				best = v
			}
			parsed++
		}
	}
	if parsed == 0 {
		// nvidia-smi ran but printed nothing usable ("[N/A]", MIG without
		// permissions, ...). Report unknown rather than "zero bytes free", which
		// would make the coordinator refuse every GPU job.
		return 0, false
	}
	return best, true
}

// DetectGPU probes nvidia-smi with a short timeout. Absence is not an error:
// ParseDock's CPU path must work on machines without a GPU or CUDA toolkit.
func DetectGPU(ctx context.Context) ([]GPU, error) {
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		return nil, nil
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(c, "nvidia-smi",
		"--query-gpu=index,name,memory.total,driver_version",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil, err
	}
	var gpus []GPU
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(line, ",")
		if len(parts) < 4 {
			continue
		}
		idx, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
		mem, _ := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64)
		gpus = append(gpus, GPU{
			Index:      idx,
			Name:       strings.TrimSpace(parts[1]),
			MemoryMB:   mem,
			DriverVers: strings.TrimSpace(parts[3]),
		})
	}
	return gpus, nil
}

// Container reports whether we appear to run inside a container.
func Container() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	b, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	return strings.Contains(string(b), "docker") || strings.Contains(string(b), "containerd") ||
		strings.Contains(string(b), "kubepods")
}
