// Package ci generates the CI pipeline `tortureu init --ci` writes
// (R-CLI-11).
//
// The only thing in here that matters is the exit-code contract. TortureU's
// verdict is a number between 0 and 4 (R-VER-7, VERDICT.md §2), and each of
// those numbers asks the reader for a different response: 1 means their
// service broke, 2 means the harness broke, 3 means the run never started,
// 4 means nothing could be attributed. A pipeline that reports "the job
// failed" throws all of that away at the exact moment someone is looking at
// it. So the generated shell branches on all five codes, says what each one
// means, and re-exits with the same number.
//
// What is verified, and what is not (be precise about this — the generated
// file makes claims about runners this repo cannot start):
//
//   - The YAML is parsed by gopkg.in/yaml.v3 in ci_test.go, and the job/step
//     structure is asserted against the parsed document, not the template.
//   - The GitLab file is checked against GitLab's documented schema keys
//     (top-level `stages:`, per-job `stage:`/`image:`/`script:`), including
//     that the job's stage exists in `stages:`.
//   - The exit-code contract is proven by *executing* the emitted script
//     under `bash` and `sh` with a stub `tortureu` that exits 0..5, and
//     asserting the status propagates and each code reports distinctly.
//   - Both files were additionally checked, once, by hand against the
//     vendors' own validators, on the output of the built binary:
//     `actionlint 1.7.12` accepted the workflow with no findings (it checks
//     the Actions schema and shellchecks every `run:` block), and
//     `check-jsonschema 0.37.4 --builtin-schema vendor.gitlab-ci` accepted
//     the GitLab pipeline. Those are one-off manual checks, not part of
//     `go test` — neither tool is a dependency of this repo.
//   - NOT verified: that GitHub Actions or GitLab CI actually *execute*
//     these files green. No runner of either kind exists here, so schema
//     validity is the strongest claim available and is the only claim made.
//     `grafana/setup-k6-action@v1` was confirmed to exist (its `v1` tag and
//     `v1.2.1` release are published on GitHub) — it was never executed.
//   - NOT verified: that `docker:dind` works on the reader's GitLab runner.
//     It needs a privileged runner, which is a property of their
//     installation; the file says so where it matters.
//   - NOT verified: that `tortureu run` completes on a hosted runner. See
//     TBD-11 for the unresolved question of how the binary gets there at
//     all; the install step is marked in-file as the line to adapt rather
//     than pointing at a distribution channel that does not exist yet.
package ci

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Providers `--ci` understands.
const (
	ProviderGitHub = "github"
	ProviderGitLab = "gitlab"
)

// Providers returns the supported provider names, in the order they are
// offered to the user.
func Providers() []string { return []string{ProviderGitHub, ProviderGitLab} }

// exitContract is the shell both pipelines run (R-CLI-11). It is shared
// verbatim rather than written per provider: two copies of the same table of
// meanings would drift, and the drift would be silent — one pipeline calling
// exit 4 "inconclusive" while the other calls it a pass is precisely the
// failure R-VER-8 exists to prevent.
//
// POSIX sh only (no bashisms): GitHub runners use bash, GitLab jobs use
// whatever the image ships, often sh. Tested under both.
//
// `set +e` around the run is required — under `set -e` the script would die
// on a non-zero exit before it could say which non-zero it was.
const exitContract = `set +e
tortureu run
code=$?
set -e

# VERDICT.md §2 / R-VER-7 — these five codes are the contract with CI.
# They are reported separately on purpose: "the build is red" does not tell
# you whether your service broke or the harness did.
case "$code" in
  0) echo "tortureu: pass (exit 0) — every assertion held" ;;
  1) echo "tortureu: FAIL (exit 1) — the system under test broke an assertion. This is a result about your service, not a harness problem." >&2 ;;
  2) echo "tortureu: ERROR (exit 2) — tortureu or an adapter failed. Nothing was proven about the service; fix the harness and re-run." >&2 ;;
  3) echo "tortureu: ABORTED (exit 3) — unclassified egress, or reset failed (DC-2). The run never started, so a green here would have meant nothing." >&2 ;;
  4) echo "tortureu: INCONCLUSIVE (exit 4) — the run failed but every finding is ambiguous: attribution is unusable. R-VER-8: this is NOT a pass." >&2 ;;
  *) echo "tortureu: UNEXPECTED exit $code — outside the documented 0-4 contract. Treated as a failure, because a result we cannot interpret is not a result we may call green." >&2 ;;
esac

# Propagate the code itself. Collapsing it to a generic 1 would erase the
# distinction the case statement above exists to preserve.
exit "$code"
`

// indent prefixes every line of s with pad, leaving blank lines empty so the
// emitted YAML has no trailing whitespace (gofmt has no opinion on YAML, but
// some linters do, and a diff full of invisible characters is noise).
func indent(s, pad string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = pad + l
		}
	}
	return strings.Join(lines, "\n")
}

// Generate renders the pipeline for provider, returning the path it belongs
// at and its content (R-CLI-11). An unknown provider is an error listing what
// is supported, in the manner R-CLI-8 set for `emit`.
func Generate(provider string) (string, []byte, error) {
	switch provider {
	case ProviderGitHub:
		return filepath.Join(".github", "workflows", "tortureu.yml"), []byte(githubWorkflow()), nil
	case ProviderGitLab:
		return ".gitlab-ci.yml", []byte(gitlabPipeline()), nil
	default:
		return "", nil, fmt.Errorf("unknown CI provider %q: supported providers are %s",
			provider, strings.Join(Providers(), ", "))
	}
}

func githubWorkflow() string {
	return `# .github/workflows/tortureu.yml — generated by ` + "`tortureu init --ci`" + `
#
# Runs one resilience experiment and lets VERDICT.md §2's exit codes decide the
# build. Codes 1 (assertion broke), 2 (harness broke), 3 (never started) and
# 4 (inconclusive) all fail — 4 included, deliberately: a green that means
# "we couldn't tell" is how a harness quietly stops finding anything.
#
# Requires a torture.yaml in the repo — run ` + "`tortureu init`" + ` to generate one.
name: tortureu

on: [push, pull_request]

jobs:
  resilience:
    # Docker is preinstalled on GitHub-hosted ubuntu runners; tortureu drives
    # the stack in docker compose, so a self-hosted runner needs it too.
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version: '1.26'

      # k6 is the load engine. Not bundled, so it is installed here.
      - uses: grafana/setup-k6-action@v1

      # EDIT ME (SPEC.md TBD-11): TortureU has no published release, image or
      # marketplace action yet, so this builds the binary from source in this
      # checkout. In a repo that is not TortureU itself there is nothing here
      # to build — replace this step with however you install tortureu.
      - name: install tortureu
        run: go build -o "$RUNNER_TEMP/tortureu" ./cmd/tortureu && echo "$RUNNER_TEMP" >> "$GITHUB_PATH"

      - name: tortureu run
        run: |
` + indent(exitContract, "          ") + `
`
}

func gitlabPipeline() string {
	return `# .gitlab-ci.yml — generated by ` + "`tortureu init --ci gitlab`" + `
#
# Runs one resilience experiment and lets VERDICT.md §2's exit codes decide the
# build. Codes 1 (assertion broke), 2 (harness broke), 3 (never started) and
# 4 (inconclusive) all fail — 4 included, deliberately: a green that means
# "we couldn't tell" is how a harness quietly stops finding anything.
#
# Requires a torture.yaml in the repo — run ` + "`tortureu init`" + ` to generate one.
#
# tortureu brings a docker compose stack up, so this job needs a Docker daemon.
# The docker:dind service below is GitLab's documented way to get one, and it
# requires a runner in privileged mode. If your runner is not privileged, point
# DOCKER_HOST at a daemon you do have and drop the services: block — the exit
# code contract in the script is what matters and is independent of this.
stages:
  - resilience

tortureu:
  stage: resilience
  image: golang:1.26
  services:
    - docker:dind
  variables:
    DOCKER_HOST: tcp://docker:2376
    DOCKER_TLS_CERTDIR: "/certs"
    DOCKER_TLS_VERIFY: "1"
    DOCKER_CERT_PATH: "/certs/client"
  before_script:
    # EDIT ME (SPEC.md TBD-11): TortureU has no published release, image or
    # package yet, so this builds the binary from source in this checkout. In a
    # repo that is not TortureU itself there is nothing here to build — replace
    # this with however you install tortureu, and install k6 alongside it.
    - go build -o /usr/local/bin/tortureu ./cmd/tortureu
  script:
` + indent(scriptList(exitContract), "    ") + `
`
}

// scriptList renders the shared contract as GitLab's ` + "`script:`" + ` list. One
// literal block scalar entry keeps the shell byte-identical to the GitHub
// step's, so both providers run the code the tests execute.
func scriptList(s string) string {
	return "- |\n" + indent(s, "  ")
}
