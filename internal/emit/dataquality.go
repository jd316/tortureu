// Soda Core — data-quality checks against the detected database
// (registry.yaml: tier delegate, when: dep:postgresql|dep:mysql|dep:snowflake,
// how: "tortureu emit soda", note "YAML-first checks, lighter than GE").
// R-CLI-8 proposed.
//
// Why this handoff earns its place: `tortureu run` now evaluates `sql:`
// assertions itself (R-CFG-18, internal/run/sqlassert.go), but only inside a
// run, only against the one database the run was pointed at, and only for the
// two engines whose client it can exec. Soda is the standing, scheduled,
// broader form of the same check — the same violation-count shape, run as its
// own pass, over data sources this project does not reach (snowflake among
// them). The two agree on the shape, so an assert means the same thing in
// both places.
//
// WHICH SODA. This emits the 3.x model (configuration.yml + checks.yml +
// `soda scan`), pinned to 3.5.6, not the current 4.x contracts model. Three
// reasons, all checked rather than assumed:
//
//  1. soda 4.x HAS NO MySQL. There is no `soda-mysql` package on PyPI (404);
//     the v4 data source table lists MySQL as "Upcoming". registry.yaml gates
//     this tool on dep:postgresql|dep:mysql|dep:snowflake, so a 4.x emitter
//     would silently serve two of the three predicates it advertises.
//  2. `soda scan` does not exist in 4.x — the CLI rejects it outright
//     ("Soda v3 commands are not supported"), and the 4.x exit codes are not
//     the 3.x ones (1 and 2 are swapped). The two models cannot be blended.
//  3. soda-core 3.5.6 is Apache-2.0; soda-core 4.x is published to PyPI with
//     "License: Proprietary". Telling a user to install the proprietary line
//     by default is a decision this file will not make for them.
//
// A 4.x contracts emitter is a separate, honest piece of work (different
// files, different CLI, a required fully-qualified dataset name). It is not
// this one, and this file does not pretend to be it.
//
// VERIFICATION STATUS (what was actually run on this host, 2026-08-09):
//
//   - VERIFIED end to end for postgres: the configuration.yml this file
//     emits was fed to a real `soda scan` (soda-core-postgres 3.5.6 in a
//     python:3.11 container) against a real postgres:16 container with a
//     real table, and soda connected, ran a check and reported the right
//     answer for both a passing and a failing check.
//   - VERIFIED, and the reason the emitted script uses Docker rather than
//     the host's python: soda-core 3.5.6 CANNOT be installed on this host.
//     Its build imports `distutils.util`, removed in Python 3.12; the host
//     runs 3.14 and `pip install soda-core-postgres==3.5.6` fails with
//     ModuleNotFoundError. The emitted script pins python:3.11 for exactly
//     this reason, which is a fact about the tool, not a preference.
//   - NOT VERIFIED: mysql and snowflake. No mysql or snowflake scan was run,
//     so their connection blocks rest on soda 3.5.6's own configuration
//     parser and documentation, not on an observed connection. Snowflake in
//     particular cannot be verified here at all: it is a hosted service and
//     there is no account.
//   - VERIFIED: the ACTIVE checks this file now emits (R-CFG-18's
//     violation-count shape, `<metric> = 0` plus a `<metric> query:`) were
//     run by a real soda-core 3.5.6 against a real postgres:16 holding an
//     orders table. With two violating rows soda reported
//     `tortureu_sql_assert_1 = 0 [FAILED] check_value: 2.0` and exited 2;
//     with the violating rows deleted it reported PASSED and exited 0. The
//     assert's SQL goes through verbatim — no column alias is required.
//
// What it refuses to invent:
//
//   - Credentials and database names. Every one is a `${VAR}` environment
//     reference (soda 3.x's own syntax). A literal password in a generated
//     file ends up committed.
//   - A table. Soda's dataset-scoped checks (`checks for TABLE:`) need a
//     table name, and detection stops at compose + lockfiles (D-3) — it has
//     never seen a schema. No row_count, no schema check, no freshness
//     check is emitted, because each would have to name a table we made up.
//   - The SHAPE of a `sql:` assert — and it no longer has to. TBD-14 is
//     resolved: R-CFG-18 makes a `sql:` expression a VIOLATION COUNT (one
//     row, one column, non-negative, holding iff zero), which is soda's
//     user-defined-metric shape exactly. Each assert is therefore emitted as
//     an ACTIVE check bounding that count at 0, with the SQL verbatim. The
//     failing-rows reading is not guessed at and not offered: a row-selecting
//     query is written `select count(*) from (<query>) t`, and `tortureu run`
//     refuses the unwrapped form for the same reason.
//   - A green result from nothing. soda 3.x exits 0 on a checks file with no
//     valid check, logging only "No valid checks found, 0 checks evaluated."
//     A CI step that passes because it checked nothing is the exact failure
//     mode this project rejects, so the emitted script greps for an active
//     check first and exits 2 if there is none.
//
// Fault translation: none. Soda reads a database; it injects nothing. Every
// fault is reported per fault with the emitter that does translate it
// (R-CLI-8).
package emit

import (
	"fmt"
	"strings"

	"github.com/jd316/tortureu/internal/config"
	"github.com/jd316/tortureu/internal/detect"
	"github.com/jd316/tortureu/internal/fault"
)

// sodaVersion is the soda-core line this emitter targets, pinned rather than
// floating: 3.5.6 is the last 3.x release and the last Apache-2.0 one, and
// every claim in this file's verification block was made against it.
const sodaVersion = "3.5.6"

// sodaPythonImage is the container the emitted script installs soda into.
// Pinned to 3.11 because soda-core 3.5.6 imports distutils, which Python
// 3.12 removed — verified by watching the install fail on this host's 3.14.
const sodaPythonImage = "python:3.11-slim"

// sodaSource maps one R-DET-9 dependency type onto everything soda needs to
// know about it: its own type name, its pip package, and the connection keys
// its configuration block takes.
type sodaSource struct {
	depType string // R-DET-9 type (postgresql, mysql, snowflake)
	sodaTy  string // soda's own `type:` value
	pkg     string // pip package providing the connector
	envPfx  string // prefix for the ${...} environment references
	// hosted is true for a data source with no host:port at all (snowflake
	// is addressed by account). Detection can never supply an address for
	// one, so the block must not have a place to put a guessed one.
	hosted bool
	// defaultPort is used ONLY when detection found the dependency but
	// derived no address, in which case the emitted block leaves the port as
	// an environment reference rather than this value. It exists to document
	// the well-known port in a comment, never to fill a field.
	wellKnownPort string
}

// sodaSources is ordered so the emitted configuration is stable, and covers
// exactly the three dep: predicates registry.yaml gates this tool on.
var sodaSources = []sodaSource{
	{depType: "postgresql", sodaTy: "postgres", pkg: "soda-core-postgres", envPfx: "SODA_POSTGRES", wellKnownPort: "5432"},
	{depType: "mysql", sodaTy: "mysql", pkg: "soda-core-mysql", envPfx: "SODA_MYSQL", wellKnownPort: "3306"},
	{depType: "snowflake", sodaTy: "snowflake", pkg: "soda-core-snowflake", envPfx: "SODA_SNOWFLAKE", hosted: true},
}

const sodaHeader = `#!/usr/bin/env bash
# Generated by tortureu emit soda. Requires: docker.
#
#   bash this-script.sh
#
# Soda Core runs YAML-declared data-quality checks against a real database, on
# its own schedule, outside a load run. Every sql: assertion in torture.yaml
# (R-CFG-18) is emitted below as an ACTIVE check: a sql: expression is a
# violation count — one row, one column, non-negative — and the invariant
# holds if and only if that count is 0, which is what each "= 0" bound says.
# ` + "`tortureu run`" + ` evaluates the same asserts in the same shape during a run;
# this is the standing form of the same question.
#
# If torture.yaml declares no sql: assertion there is nothing to check, and
# this script exits without scanning on purpose: soda exits 0 on a checks file
# with no valid check, and a green CI step that checked nothing is worse than
# a red one.
#
# No credentials are emitted. Every one is a ${VAR} reference soda 3.x
# resolves from the environment at scan time.
#
# at:/for: are NOT scheduled by this script — this is a delegate-tier handoff
# (real output, separate timing, registry.yaml).
set -euo pipefail
`

// Soda emits the data-quality handoff described in this file's header.
func Soda(cfg *config.Config, sys *detect.System) (string, error) {
	if sys == nil {
		return "# tortureu emit soda: the system could not be detected, so which database (if any)\n" +
			"# this repo talks to is unknown — which is not the same as knowing it talks to none;\n" +
			"# nothing to emit.\n", nil
	}
	var found []sodaSource
	for _, src := range sodaSources {
		if _, ok := findDep(sys, src.depType); ok {
			found = append(found, src)
		}
	}
	if len(found) == 0 {
		return "# tortureu emit soda: no postgresql, mysql or snowflake dependency was detected\n" +
			"# (dep:postgresql|dep:mysql|dep:snowflake), and soda checks a database or nothing;\n" +
			"# nothing to emit.\n", nil
	}

	var faultNotes strings.Builder
	for _, f := range cfg.Faults {
		if _, err := fault.Translate(f); err != nil {
			return "", fmt.Errorf("emit soda: %w", err)
		}
		faultNotes.WriteString(atComment(f))
		faultNotes.WriteString(skipComment("soda", f,
			"soda reads a database and injects nothing; use \"tortureu emit pumba\", \"netem\" or \"iptables\" for this fault"))
	}

	var b strings.Builder
	b.WriteString(sodaHeader)
	b.WriteString(`
work="${TORTUREU_SODA_WORKDIR:-./soda-work}"
mkdir -p "$work"

`)
	b.WriteString("cat > \"$work/configuration.yml\" <<'SODA_CONFIGURATION'\n")
	b.WriteString(sodaConfiguration(sys, found))
	b.WriteString("SODA_CONFIGURATION\n")

	sqlAsserts := sodaSQLAsserts(cfg)
	b.WriteString("\ncat > \"$work/checks.yml\" <<'SODA_CHECKS'\n")
	b.WriteString(sodaChecks(sqlAsserts))
	b.WriteString("SODA_CHECKS\n")

	// The primary data source: the first detected one, in sodaSources order.
	// Named explicitly rather than defaulted, because `soda scan -d` takes
	// exactly one and picking silently would scan a database the user did
	// not mean when a repo talks to two.
	primary := found[0]
	fmt.Fprintf(&b, `
# soda scan takes ONE data source. This repo has %d that soda covers; the
# emitted configuration carries a block for each, and this scans %q. Override
# with TORTUREU_SODA_DATA_SOURCE to scan another block instead of silently
# choosing for you.
data_source="${TORTUREU_SODA_DATA_SOURCE:-%s}"
`, len(found), primary.sodaTy, primary.sodaTy)

	b.WriteString(`
# A checks file whose every line is blank or a comment makes soda 3.x log
# "No valid checks found, 0 checks evaluated." and exit 0 — a pass that
# checked nothing. Refuse instead.
if ! grep -qv -e '^[[:space:]]*$' -e '^[[:space:]]*#' "$work/checks.yml"; then
  echo "tortureu emit soda: $work/checks.yml has no active check, so a scan would print" >&2
  echo "  \"No valid checks found, 0 checks evaluated.\" and exit 0 — a green result that" >&2
  echo "  checked nothing. Uncomment the check shape you mean and run this again." >&2
  exit 2
fi

# --network: soda runs in a container and must reach the database at the
# address detection derived, which for a compose dependency is a service name
# resolvable only on that compose network. There is no honest default, so
# this is required rather than guessed — pointing soda at the wrong database
# and reporting the answer would be worse than not running.
if [ -z "${TORTUREU_SODA_DOCKER_NETWORK:-}" ]; then
  echo "tortureu emit soda: TORTUREU_SODA_DOCKER_NETWORK is required." >&2
  echo "  The detected address is a compose service name, resolvable only on that project's" >&2
  echo "  network (docker network ls). Set it to that network and run this again." >&2
  exit 2
fi
`)

	pkgs := make([]string, 0, len(found))
	for _, src := range found {
		pkgs = append(pkgs, fmt.Sprintf("%s==%s", src.pkg, sodaVersion))
	}
	fmt.Fprintf(&b, `
# python:3.11 is pinned, not a preference: soda-core %s imports
# distutils.util, which Python 3.12 removed, so the install fails outright on
# a modern interpreter (observed on this machine's 3.14).
docker run --rm \
  --network "$TORTUREU_SODA_DOCKER_NETWORK" \
  -v "$(cd "$work" && pwd)":/soda -w /soda \
  -e %s_USER -e %s_PASSWORD -e %s_DATABASE %s\
  %s \
  sh -c 'pip install --quiet %s && soda scan -d "'"$data_source"'" -c configuration.yml checks.yml -srf /soda/scan-results.json'

# soda 3.x exit codes: 0 all checks passed, 1 warning(s), 2 failure(s),
# 3 runtime error. Note 1 and 2 are NOT R-VER-7's meanings and are not
# remapped here — this is a handoff, and soda's result is soda's to report.
# The full JSON result is in $work/scan-results.json (-srf is soda 3.x's only
# machine-readable output; there is no --json).
`, sodaVersion, primary.envPfx, primary.envPfx, primary.envPfx, sodaExtraEnv(primary), sodaPythonImage, strings.Join(pkgs, " "))

	if len(sqlAsserts) == 0 {
		b.WriteString("\n# NOTE: torture.yaml declares no sql: assertions (R-CFG-18), so the checks file\n" +
			"# above is a scaffold only. Nothing was inferred from the schema — detection stops\n" +
			"# at compose and lockfiles (D-3) and has never seen a table.\n")
	}
	if faultNotes.Len() > 0 {
		b.WriteString("\n# faults declared in torture.yaml that this emit does NOT translate\n" +
			"# (listed, never dropped — R-CLI-8):\n")
		b.WriteString(faultNotes.String())
	}
	return b.String(), nil
}

// sodaExtraEnv adds the environment variables a hosted source needs beyond
// user/password/database, so the docker invocation passes exactly what the
// emitted configuration block references and nothing more.
func sodaExtraEnv(src sodaSource) string {
	if !src.hosted {
		return ""
	}
	return fmt.Sprintf("-e %s_ACCOUNT -e %s_WAREHOUSE -e %s_ROLE ", src.envPfx, src.envPfx, src.envPfx)
}

// sodaConfiguration renders configuration.yml: one `data_source <name>:`
// block per detected source. soda 3.x requires the block name to match
// ^[a-z_][a-z_0-9]+$, which soda's own type names already satisfy.
func sodaConfiguration(sys *detect.System, found []sodaSource) string {
	var b strings.Builder
	for _, src := range found {
		dep, _ := findDep(sys, src.depType)
		fmt.Fprintf(&b, "data_source %s:\n", src.sodaTy)
		fmt.Fprintf(&b, "  type: %s\n", src.sodaTy)
		if src.hosted {
			// Snowflake is addressed by account, never host:port. R-DET-13
			// makes it a lockfile-only dependency, so detection proves only
			// that this repo has a snowflake client — never which account.
			fmt.Fprintf(&b, "  account: ${%s_ACCOUNT}\n", src.envPfx)
			fmt.Fprintf(&b, "  warehouse: ${%s_WAREHOUSE}\n", src.envPfx)
			fmt.Fprintf(&b, "  role: ${%s_ROLE}\n", src.envPfx)
		} else {
			host, port := hostPort(dep.Address)
			if host == "" {
				// Detected, but R-DET-4 derived no address. An address must
				// not be assumed from the service name: scanning the wrong
				// database and reporting the result is worse than refusing.
				fmt.Fprintf(&b, "  # detection found this dependency but derived no host:port for it\n")
				fmt.Fprintf(&b, "  # (R-DET-4); its well-known port is %s, which is NOT filled in here.\n", src.wellKnownPort)
				fmt.Fprintf(&b, "  host: ${%s_HOST}\n", src.envPfx)
				fmt.Fprintf(&b, "  port: '${%s_PORT}'\n", src.envPfx)
			} else {
				fmt.Fprintf(&b, "  host: %s\n", host)
				if port == "" {
					fmt.Fprintf(&b, "  # the detected address carried no port; soda's own default applies.\n")
				} else {
					fmt.Fprintf(&b, "  port: '%s'\n", port)
				}
			}
		}
		fmt.Fprintf(&b, "  username: ${%s_USER}\n", src.envPfx)
		fmt.Fprintf(&b, "  password: ${%s_PASSWORD}\n", src.envPfx)
		fmt.Fprintf(&b, "  database: ${%s_DATABASE}\n", src.envPfx)
		if src.depType == "postgresql" {
			// mysql has no `schema:` in soda's postgres-style sense (its
			// database IS the schema), and snowflake's is optional; only
			// postgres gets one, and it is still not guessed as "public".
			fmt.Fprintf(&b, "  schema: ${%s_SCHEMA}\n", src.envPfx)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// sodaSQLAsserts returns every sql: assert entry (R-CFG-18) in declaration
// order — the assertions the run itself can only report as unevaluated.
func sodaSQLAsserts(cfg *config.Config) []string {
	var out []string
	for _, entry := range cfg.Assert {
		if expr, ok := entry["sql"].(string); ok {
			out = append(out, expr)
		}
	}
	return out
}

// sodaChecks renders checks.yml: one ACTIVE check per sql: assert, in the
// shape R-CFG-18 mandates. A `sql:` expression is a violation count — one
// row, one column, a non-negative number, holding iff it is zero — which is
// exactly soda's user-defined-metric shape (`<metric> = 0` plus a
// `<metric> query:`), so the translation is one-to-one and needs no guess.
// Verified against a real soda-core 3.5.6 and a real postgres:16 (see this
// file's VERIFICATION STATUS): the emitted check reports check_value 2.0 and
// exits 2 with two violating rows present, and exits 0 with none — with the
// assert's SQL passed through verbatim and no column alias required.
func sodaChecks(sqlAsserts []string) string {
	var b strings.Builder
	b.WriteString("# Generated by tortureu emit soda — soda 3.x SodaCL.\n" +
		"#\n" +
		"# Each check below is one sql: assertion from torture.yaml (R-CFG-18), whose\n" +
		"# expression is a VIOLATION COUNT: exactly one row, exactly one column, a\n" +
		"# non-negative number, and the invariant holds if and only if it is 0. That is\n" +
		"# what `<metric> = 0` bounds here, and the SQL is passed through verbatim.\n" +
		"#\n" +
		"# If a check errors with \"query does not return a single value\", the SQL is not\n" +
		"# in R-CFG-18's shape — a row-selecting query becomes\n" +
		"#   select count(*) from (<your query>) t\n" +
		"# and `tortureu run` refuses the same shape for the same reason (TBD-14).\n")
	if len(sqlAsserts) == 0 {
		b.WriteString("#\n# torture.yaml declares no sql: assertions, so there is no check to emit and\n" +
			"# nothing here is active. Add one (R-CFG-18) and re-run tortureu emit soda;\n" +
			"# until then the generated script refuses to scan rather than passing on a\n" +
			"# checks file soda would evaluate zero checks from.\n")
		return b.String()
	}
	b.WriteString("\nchecks:\n")
	for i, expr := range sqlAsserts {
		fmt.Fprintf(&b, "  # %s\n", sodaCheckName(expr))
		fmt.Fprintf(&b, "  - %s = 0:\n", sodaMetricName(i))
		fmt.Fprintf(&b, "      %s query: |\n", sodaMetricName(i))
		for _, line := range strings.Split(strings.TrimRight(expr, "\n"), "\n") {
			fmt.Fprintf(&b, "        %s\n", line)
		}
	}
	return b.String()
}

// sodaCheckName is the human label on a failed-rows check: the assertion's
// own SQL, truncated, so the check in soda's output is traceable back to the
// torture.yaml line that asked for it.
func sodaCheckName(expr string) string {
	one := strings.Join(strings.Fields(expr), " ")
	if len(one) > 70 {
		one = one[:70] + "..."
	}
	return "torture.yaml sql: " + one
}

// sodaMetricName is the identifier shape B needs. SodaCL requires the metric
// name in the check and in the `<name> query:` key to match exactly, and it
// must be a bare identifier — the SQL itself cannot be the name.
func sodaMetricName(i int) string {
	return fmt.Sprintf("tortureu_sql_assert_%d", i+1)
}

func init() {
	// needsSystem: true. The data source's address is a detection fact
	// (R-DET-4) and this emitter refuses rather than guessing one, exactly
	// as sysbench/memtier/kafka-load do.
	Register("soda", Soda, true)
}
