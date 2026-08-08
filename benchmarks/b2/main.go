// Command b2 is the B2 harness-overhead benchmark (BENCHMARKS.md §B2): what
// does routing through TortureU's proxy cost when no fault is active? It
// drives the same scenario (a client holding N concurrent persistent
// connections to a known-good TCP echo service, round-tripping a small
// fixed payload as fast as possible for a fixed wall-clock window) through
// three configurations:
//
//  1. direct    -- client dials echo with no proxy on the connection path
//  2. toxiproxy -- client dials the real Toxiproxy proxy, no toxic installed
//  3. tortureu  -- same proxy path, but with a real (zero-effect) toxic
//     applied through the actual production fault.Translate ->
//     fault.Manager -> ToxiproxyApplier path (CombinedApplier, same as
//     internal/run's own Docker-backed tests), isolating "TortureU's own
//     orchestration is active" from "a fault is distorting traffic" (a
//     separate, already-measured question: B1)
//
// and reports p50/p95/p99 latency deltas and requests/sec against the direct
// baseline, plus the load generator's OWN ceiling on this machine (fd limit,
// ephemeral port range, CPU) -- BENCHMARKS.md §B2's own rule: a tool that
// reports "your backend maxes at 2k rps" when the *generator* maxed out is
// worse than no tool.
//
// Results are written to benchmarks/results/<date>-<commit>-b2.json, with
// date and commit both read from the environment at run time, never
// hardcoded.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const loadWorkers = 8
const loadDuration = 5 * time.Second

// platform mirrors b1's Platform label (rule: label the platform on every
// published number). Duplicated rather than imported: b1 and b2 are both
// `package main` and cannot import each other's unexported types.
type platform struct {
	OS            string `json:"os"`
	DockerVersion string `json:"docker_version"`
	CPUModel      string `json:"cpu_model"`
	CPUCores      int    `json:"cpu_cores"`
}

func gatherPlatform() platform {
	p := platform{}
	if out, err := exec.Command("uname", "-sr").Output(); err == nil {
		p.OS = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").Output(); err == nil {
		p.DockerVersion = strings.TrimSpace(string(out))
	}
	if raw, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "model name") {
				if parts := strings.SplitN(line, ":", 2); len(parts) == 2 {
					p.CPUModel = strings.TrimSpace(parts[1])
				}
				break
			}
		}
	}
	if out, err := exec.Command("nproc").Output(); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil {
			p.CPUCores = n
		}
	}
	return p
}

func gitShortCommit() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func utcDate() string {
	out, err := exec.Command("date", "-u", "+%Y-%m-%d").Output()
	if err != nil {
		return time.Now().UTC().Format("2006-01-02")
	}
	return strings.TrimSpace(string(out))
}

func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

type resultsFile struct {
	TimestampUTC     string               `json:"timestamp_utc"`
	Commit           string               `json:"commit"`
	Platform         platform             `json:"platform"`
	Scenario         string               `json:"scenario"`
	LoadWorkers      int                  `json:"load_workers"`
	LoadDurationSec  float64              `json:"load_duration_sec"`
	GeneratorCeiling generatorCeilingInfo `json:"generator_ceiling"`
	Configs          []configResult       `json:"configs"`
}

func runConfig(name string, m mode) (configResult, *stats) {
	cr := configResult{Config: name}
	var st *stats
	res := withStack("b2-"+name, m, func(s *b2Stack) result {
		lr, err := s.runLoad(loadWorkers, loadDuration)
		if err != nil {
			return result{unmeasuredNote: fmt.Sprintf("runLoad: %v", err)}
		}
		computed := computeStats(lr.LatenciesMs, lr.WallSeconds, loadWorkers)
		st = &computed
		cr.Stats = &computed
		cr.WorkerErrors = lr.WorkerErrors
		cr.Verdict = "measured"
		if lr.WorkerErrors > 0 {
			cr.note("%d/%d workers hit an error during the run (see raw n=%d); rps/latency below reflect only successful round trips", lr.WorkerErrors, loadWorkers, lr.N)
		}
		return result{}
	})
	if res.unmeasuredNote != "" && cr.Verdict == "" {
		cr.Verdict = "unmeasured"
		cr.note("%s", res.unmeasuredNote)
	}
	return cr, st
}

func main() {
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		fmt.Fprintln(os.Stderr, "b2: docker compose not available; cannot run the harness-overhead benchmark")
		os.Exit(1)
	}

	plat := gatherPlatform()
	fmt.Printf("b2: platform: %s | docker %s | %s x%d\n", plat.OS, plat.DockerVersion, plat.CPUModel, plat.CPUCores)

	var ceiling generatorCeilingInfo
	fmt.Println("b2: direct ...")
	directResult, directStats := runConfig("direct", modeDirect)
	// generatorCeiling needs a live client container; grab it from a small
	// extra direct-mode stack rather than threading the container name back
	// out of withStack's closure return value.
	_ = withStack("b2-ceiling", modeDirect, func(s *b2Stack) result {
		ceiling = s.generatorCeiling()
		return result{}
	})
	ceiling.CPUModel, ceiling.CPUCores = plat.CPUModel, plat.CPUCores

	fmt.Println("b2: toxiproxy (no toxic) ...")
	toxiproxyResult, _ := runConfig("toxiproxy", modeToxiproxy)

	fmt.Println("b2: tortureu (orchestrated, zero-effect toxic) ...")
	tortureuResult, _ := runConfig("tortureu", modeOrchestrated)

	configs := []configResult{directResult, toxiproxyResult, tortureuResult}
	if directStats != nil {
		for i := range configs {
			if configs[i].Config == "direct" || configs[i].Stats == nil {
				continue
			}
			configs[i].DeltaP50Ms = configs[i].Stats.P50 - directStats.P50
			configs[i].DeltaP95Ms = configs[i].Stats.P95 - directStats.P95
			configs[i].DeltaP99Ms = configs[i].Stats.P99 - directStats.P99
			if directStats.RPS > 0 {
				configs[i].DeltaRPSPct = (configs[i].Stats.RPS - directStats.RPS) / directStats.RPS * 100
			}
		}
	} else {
		for i := range configs {
			if configs[i].Config != "direct" {
				configs[i].note("no direct baseline was measured, so no delta could be computed")
			}
		}
	}

	for _, c := range configs {
		if c.Stats != nil {
			fmt.Printf("  %-10s %-10s p50=%.2fms p95=%.2fms p99=%.2fms rps=%.1f (delta p50=%.2fms p95=%.2fms p99=%.2fms rps=%.1f%%)\n",
				c.Config, strings.ToUpper(c.Verdict), c.Stats.P50, c.Stats.P95, c.Stats.P99, c.Stats.RPS,
				c.DeltaP50Ms, c.DeltaP95Ms, c.DeltaP99Ms, c.DeltaRPSPct)
		} else {
			fmt.Printf("  %-10s %-10s %v\n", c.Config, strings.ToUpper(c.Verdict), c.Notes)
		}
	}
	fmt.Printf("b2: generator ceiling: fd_limit=%s ephemeral_port_range=%s cpu=%s x%d\n",
		ceiling.FDLimit, ceiling.EphemeralPortRange, ceiling.CPUModel, ceiling.CPUCores)

	out := resultsFile{
		TimestampUTC:     time.Now().UTC().Format(time.RFC3339),
		Commit:           gitShortCommit(),
		Platform:         plat,
		Scenario:         fmt.Sprintf("%d concurrent persistent TCP connections, 64-byte echo round trips, %s sustained window", loadWorkers, loadDuration),
		LoadWorkers:      loadWorkers,
		LoadDurationSec:  loadDuration.Seconds(),
		GeneratorCeiling: ceiling,
		Configs:          configs,
	}

	resultsDir := filepath.Join(repoRoot(), "benchmarks", "results")
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "b2: mkdir results dir:", err)
		os.Exit(1)
	}
	filename := fmt.Sprintf("%s-%s-b2.json", utcDate(), out.Commit)
	outPath := filepath.Join(resultsDir, filename)
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "b2: marshal results:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(outPath, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "b2: write results:", err)
		os.Exit(1)
	}
	fmt.Println("b2: results written to", outPath)

	for _, c := range configs {
		if c.Verdict == "unmeasured" {
			fmt.Printf("b2: %s config UNMEASURED: %v\n", c.Config, c.Notes)
		}
	}
}
