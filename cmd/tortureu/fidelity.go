package main

import "runtime"

// fidelityNote states whether B1's fault-fidelity measurements cover the
// platform this binary is running on (R-CLI-21).
//
// Every published B1 number — latency within ±10ms, bandwidth within ±5%, CPU
// quota within ±5% — was measured on Linux with cgroup v2. Fault delivery is
// kernel work: netem shapes the packets, cgroup quota throttles the CPU, and
// SIGSTOP freezes the container. On macOS and Windows that kernel is the
// Docker VM's, not the user's, and nobody has measured it.
//
// Saying so costs one line and is the same rule this project applies to every
// other number it reports: unmeasured is stated, never inferred.
func fidelityNote(goos string) string {
	if goos == "linux" {
		return "been measured on this platform (Linux/cgroup v2) — see BENCHMARKS.md B1: all 7 fault verbs land within tolerance"
	}
	return "NOT been measured on " + goos + " — B1's numbers are Linux/cgroup v2 only. " +
		"Orchestration, egress isolation and attribution behave the same everywhere; what is unverified " +
		"here is the magnitude of an injected fault (netem, cgroup quota and pause run in the Docker VM's " +
		"kernel). Treat a fault's size as approximate until `make bench` is run on this platform."
}

// currentFidelityNote is fidelityNote for the running binary.
func currentFidelityNote() string { return fidelityNote(runtime.GOOS) }
