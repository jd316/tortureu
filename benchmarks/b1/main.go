// Command b1 is the B1 fault-fidelity benchmark (BENCHMARKS.md §B1): for
// each inject: verb, drive a known-good TCP echo service through the real
// fault path (ComposeTopologyApplier.Apply + egress.BuildTopology +
// fault.Translate + fault.Manager + ToxiproxyApplier/DockerApplier/
// CombinedApplier — exactly as internal/run's own Docker-backed tests do)
// and measure requested vs. observed effect at the client.
//
// Results are written to benchmarks/results/<date>-<commit>.json, with date
// and commit both read from the environment at run time (`date -u`, `git
// rev-parse --short HEAD`), never hardcoded.
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

// Platform is the machine/software label every published number carries
// (rule 2: label the platform on every published number).
type Platform struct {
	OS            string `json:"os"`             // uname -sr
	DockerVersion string `json:"docker_version"` // docker version --format {{.Server.Version}}
	CPUModel      string `json:"cpu_model"`
	CPUCores      int    `json:"cpu_cores"`
	CgroupVersion string `json:"cgroup_version"` // "v1" | "v2" | "unknown"
}

func gatherPlatform() Platform {
	p := Platform{CgroupVersion: "unknown"}
	if out, err := exec.Command("uname", "-sr").Output(); err == nil {
		p.OS = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").Output(); err == nil {
		p.DockerVersion = strings.TrimSpace(string(out))
	}
	if raw, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "model name") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
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
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err == nil {
		p.CgroupVersion = "v2"
	} else if _, err := os.Stat("/sys/fs/cgroup/cpu"); err == nil {
		p.CgroupVersion = "v1"
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

// ResultsFile is the full JSON document written to benchmarks/results/.
type ResultsFile struct {
	Timestamp string   `json:"timestamp_utc"`
	Commit    string   `json:"commit"`
	Platform  Platform `json:"platform"`
	Results   []Result `json:"results"`
}

func main() {
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		fmt.Fprintln(os.Stderr, "b1: docker compose not available; cannot run the fault-fidelity benchmark")
		os.Exit(1)
	}

	platform := gatherPlatform()
	fmt.Printf("b1: platform: %s | docker %s | %s x%d | cgroup %s\n",
		platform.OS, platform.DockerVersion, platform.CPUModel, platform.CPUCores, platform.CgroupVersion)

	var results []Result

	fmt.Println("b1: latency + jitter ...")
	lat, jit := runLatencyJitter()
	results = append(results, lat, jit)

	fmt.Println("b1: bandwidth ...")
	results = append(results, runBandwidth())

	fmt.Println("b1: down ...")
	results = append(results, runDown())

	fmt.Println("b1: pause ...")
	results = append(results, runPause())

	fmt.Println("b1: kill ...")
	results = append(results, runKill())

	fmt.Println("b1: cpu ...")
	if err := buildCPUEchoImage(); err != nil {
		fmt.Println("b1: cpu ... unmeasured (image build failed):", err)
		results = append(results, Result{Verb: "cpu", Requested: "90% of quota", Tolerance: "±5% (cgroup cpu.stat)", Verdict: "unmeasured", Notes: []string{err.Error()}})
	} else {
		results = append(results, runCPU())
	}

	for _, r := range results {
		fmt.Printf("  %-10s %-8s requested=%s measured=%v\n", r.Verb, strings.ToUpper(r.Verdict), r.Requested, r.Measured)
	}

	out := ResultsFile{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Commit:    gitShortCommit(),
		Platform:  platform,
		Results:   results,
	}

	resultsDir := filepath.Join(repoRoot(), "benchmarks", "results")
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "b1: mkdir results dir:", err)
		os.Exit(1)
	}
	filename := fmt.Sprintf("%s-%s.json", utcDate(), out.Commit)
	outPath := filepath.Join(resultsDir, filename)
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "b1: marshal results:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(outPath, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "b1: write results:", err)
		os.Exit(1)
	}
	fmt.Println("b1: results written to", outPath)

	misses := 0
	for _, r := range results {
		if r.Verdict == "miss" {
			misses++
		}
	}
	if misses > 0 {
		fmt.Printf("b1: %d/%d verbs MISSED tolerance — see %s for details (this is expected and reported honestly, not a harness bug)\n", misses, len(results), outPath)
	}
}

// repoRoot finds the module root by walking up from the working directory
// looking for go.mod, so `go run ./benchmarks/b1/...` writes results to
// benchmarks/results/ regardless of the caller's cwd.
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
