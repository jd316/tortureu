.PHONY: bench eval bench-ci

# B1 fault fidelity (BENCHMARKS.md §B1). Needs Docker; brings up and tears
# down several short-lived compose stacks. Writes
# benchmarks/results/<date>-<commit>.json.
bench:
	go run ./benchmarks/b1/...

# B2 overhead and E1 attribution (BENCHMARKS.md). Not built yet — do not run
# this expecting real numbers.
eval:
	@echo "make eval: not implemented (E1/E2 are not built — see BENCHMARKS.md 'Running them')" >&2
	@exit 1

# B2 overhead + an E1 subset, meant to gate PRs on regression. Not built yet.
bench-ci:
	@echo "make bench-ci: not implemented (see BENCHMARKS.md 'Running them')" >&2
	@exit 1
