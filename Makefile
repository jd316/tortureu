.PHONY: bench eval bench-ci

# B1 fault fidelity + B2 harness overhead (BENCHMARKS.md §B1, §B2). Needs
# Docker; brings up and tears down several short-lived compose stacks per
# benchmark. Writes benchmarks/results/<date>-<commit>.json (B1) and
# benchmarks/results/<date>-<commit>-b2.json (B2).
bench:
	go run ./benchmarks/b1/...
	go run ./benchmarks/b2/...

# E1 attribution (BENCHMARKS.md §E1). Needs Docker; builds and tears down one
# compose stack per corpus case. Case 8 is the control: it must produce zero
# findings, and this target fails if it does not — a tool that invents a
# finding on a healthy system is worse than one that misses a real defect.
eval:
	./evals/run_case.sh

# B2 overhead + an E1 subset, meant to gate PRs on regression. Not built yet.
bench-ci:
	@echo "make bench-ci: not implemented (see BENCHMARKS.md 'Running them')" >&2
	@exit 1
