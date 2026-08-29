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
//   - The install step is exercised too, not just asserted on: while
//     ReleaseVersion is empty the emitted shell is executed under bash and
//     sh with a *working* stub tortureu on PATH, and must still exit 2 and
//     say why. A step that merely fell through would come back green there.
//   - NOT verified: that `tortureu run` completes on a hosted runner, and
//     NOT verified that the release download or the image pull succeed —
//     no release, archive or image has been published (TBD-11), which is
//     precisely why the generated install step fails loudly instead of
//     fetching one. The download shell, the checksum verification and the
//     image reference are all unexercised against a real artefact until a
//     maintainer pushes the first tag.
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

// ReleaseVersion is the TortureU release the generated pipeline installs
// (R-CLI-11). It is a git tag — `v0.1.0` — and it is the single place the pin
// lives: the GitHub workflow downloads that release's archive, the GitLab job
// runs in the image published at the same tag.
//
// Empty means **no release has been published yet**, which is the state this
// repo is in until a maintainer pushes the first tag. While it is empty the
// generated install step fails the job with exit 2 and says so (R-CLI-11,
// SPEC.md TBD-11) — it does not fall back to `latest`, and it does not emit a
// download of a URL that does not resolve. Set this constant to the tag as
// part of releasing, after confirming the artefacts are public.
const ReleaseVersion = "v0.1.2"

// releaseRepo is the GitHub repository the artefacts come from. Named once so
// the download URL, the image reference and the `go install` path in the
// generated comments cannot drift apart.
// releaseRepo is the GitHub repository the artefacts come from, and also the
// GHCR image path. They were separate constants while the repository was
// named "TortureU": GHCR forbids uppercase image names, so the two genuinely
// differed. The repository is lowercase now, so one name serves both.
const releaseRepo = "jd316/tortureu"

// Generate renders the pipeline for provider, returning the path it belongs
// at and its content (R-CLI-11). An unknown provider is an error listing what
// is supported, in the manner R-CLI-8 set for `emit`.
func Generate(provider string) (string, []byte, error) {
	return generate(provider, ReleaseVersion)
}

// generate is Generate with the release pin passed in, so both sides of the
// "a release exists / does not exist yet" fork are reachable from a test
// without a tag having to exist.
func generate(provider, version string) (string, []byte, error) {
	switch provider {
	case ProviderGitHub:
		return filepath.Join(".github", "workflows", "tortureu.yml"), []byte(githubWorkflow(version)), nil
	case ProviderGitLab:
		return ".gitlab-ci.yml", []byte(gitlabPipeline(version)), nil
	default:
		return "", nil, fmt.Errorf("unknown CI provider %q: supported providers are %s",
			provider, strings.Join(Providers(), ", "))
	}
}

// noReleaseShell is the install step emitted while ReleaseVersion is empty
// (R-CLI-11). POSIX sh, no downloads, exit 2 — the harness could not be
// installed, so nothing was proven about the service, which is exactly what
// R-VER-7 gives code 2 to mean.
//
// The alternative — emitting the download anyway — would report a release
// that was never cut as a 404, i.e. as somebody else's outage. The other
// alternative, warning and continuing, would produce a green pipeline that
// ran no experiment. Both are worse than a red build with a sentence in it.
const noReleaseShell = `echo "tortureu: no published release exists yet (SPEC.md TBD-11)." >&2
echo "  Nothing was installed, so nothing was proven. Exit 2 = harness error (VERDICT.md 2)." >&2
echo "  Replace this step with whichever install route you prefer, pinned to a tag:" >&2
echo "    - go install github.com/` + releaseRepo + `/cmd/tortureu@vX.Y.Z   (needs Go on the runner)" >&2
echo "    - download tortureu_X.Y.Z_linux_amd64.tar.gz from the release and verify it against checksums.txt" >&2
echo "    - run this job inside the ghcr.io/` + releaseRepo + `:vX.Y.Z image" >&2
exit 2
`

// githubInstallShell downloads the pinned release archive and verifies it
// against the release's own checksums.txt before anything is executed. The
// checksum is not decoration: this step puts a binary on PATH that then runs
// with access to the repo's Docker daemon.
func githubInstallShell(version string) string {
	return `set -euo pipefail
version="` + version + `"
base="https://github.com/` + releaseRepo + `/releases/download/${version}"
archive="tortureu_${version#v}_linux_amd64.tar.gz"
cd "$RUNNER_TEMP"
curl -fsSLO "$base/$archive"
curl -fsSLO "$base/checksums.txt"
grep " ${archive}$" checksums.txt | sha256sum -c -
tar -xzf "$archive" tortureu
echo "$RUNNER_TEMP" >> "$GITHUB_PATH"
`
}

func githubWorkflow(version string) string {
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

      # k6 is the load engine. It is AGPL-3 and a separate program (SPEC.md
      # §10), so it is installed here rather than bundled with tortureu.
      - uses: grafana/setup-k6-action@v1

` + indent(githubInstallStep(version), "      ") + `

      - name: tortureu run
        run: |
` + indent(exitContract, "          ") + `
`
}

// githubInstallStep is the `install tortureu` step: the pinned, checksum-
// verified download once a release exists, and the loud failure until then
// (R-CLI-11).
func githubInstallStep(version string) string {
	if version == "" {
		return `# SPEC.md TBD-11 / R-CLI-11: no TortureU release is published yet, so
# there is nothing to install and this step fails the job rather than
# fetching a URL that does not resolve. The routes it prints are real —
# pick one, pin it to a tag, and replace this step.
- name: install tortureu
  run: |
` + indent(noReleaseShell, "    ")
	}
	return `# tortureu ` + version + `, from its GitHub release, verified against that
# release's own checksums.txt. Pinned on purpose: a harness that updates
# itself under the pipeline makes every regression ambiguous.
- name: install tortureu
  run: |
` + indent(githubInstallShell(version), "    ")
}

func gitlabPipeline(version string) string {
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
#
# k6 is the load engine and is AGPL-3, a separate program from this MIT one
# (SPEC.md §10), so it is not in the tortureu image. Install it in this job —
# or use a runner image that already has it — before the run step needs it.
stages:
  - resilience

tortureu:
  stage: resilience
` + gitlabInstall(version) + `  services:
    - docker:dind
  variables:
    DOCKER_HOST: tcp://docker:2376
    DOCKER_TLS_CERTDIR: "/certs"
    DOCKER_TLS_VERIFY: "1"
    DOCKER_CERT_PATH: "/certs/client"
  script:
` + indent(scriptList(exitContract), "    ") + `
`
}

// gitlabInstall renders the job's image (and, before the first release, the
// before_script that fails the job) — GitLab's install step is the image it
// runs in (R-CLI-11).
//
// Once a release exists the job runs *inside* ghcr.io/<repo>:<tag>, which
// carries tortureu and the Docker CLI. Nothing is downloaded at job time, and
// the pin is the image tag.
func gitlabInstall(version string) string {
	if version == "" {
		return `  # SPEC.md TBD-11 / R-CLI-11: no tortureu image or release is published yet,
  # so this job cannot install one and fails instead of pulling a tag that
  # does not exist. docker:cli is a placeholder image with the Docker CLI in
  # it; once a release exists, replace both it and this before_script with
  #   image: ghcr.io/` + releaseRepo + `:vX.Y.Z
  image: docker:cli
  before_script:
` + indent(scriptList(noReleaseShell), "    ") + `
`
	}
	return `  # tortureu ` + version + `: the job runs inside the released image, so the
  # binary arrives with the runner and the pin is the image tag. Nothing is
  # downloaded mid-job and nothing floats.
  image: ghcr.io/` + releaseRepo + `:` + version + `
`
}

// scriptList renders the shared contract as GitLab's ` + "`script:`" + ` list. One
// literal block scalar entry keeps the shell byte-identical to the GitHub
// step's, so both providers run the code the tests execute.
func scriptList(s string) string {
	return "- |\n" + indent(s, "  ")
}
