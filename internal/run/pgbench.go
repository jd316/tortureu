// pgbench.go is the real DBLoadRunner behind `tortureu run --db-load`
// (R-EXE-26): PostgreSQL's own pgbench, driven as a separate process while
// the HTTP load and the faults run.
//
// Every behaviour encoded here was measured against pgbench 18.4 driving a
// real postgres:16-alpine container, not assumed:
//
//   - a conninfo URI is accepted in place of a bare database name, so the
//     caller's `-db-url` is passed straight through and nothing is composed
//     from detected values (R-EXE-26 forbids guessing credentials);
//   - a natural end (`-T` elapsed) exits 0 and prints
//     "tps = 476.853522 (without initial connection time)" plus
//     "number of transactions actually processed: 2374";
//   - a bad DSN exits 1 with "connection to server ... failed: FATAL: ...",
//     which is a *tool* failure (R-VER-2's error, not a SUT failure);
//   - a run cut short (the load ended first, so this package kills it)
//     prints **no** final summary at all — measured for SIGINT, which exits
//     130 with nothing after the last progress line, and the same holds for
//     the kill used here. Its throughput is therefore read from the last
//     `-P 5` progress line ("progress: 5.0 s, 451.8 tps, ...") and the
//     result is marked CutShort rather than passed off as a completed
//     measurement.
//
// Container mode (InNamespaceOf, TBD-12) drives the identical pgbench CLI
// contract inside the official postgres image, joined to the database
// container's own network namespace — so every measured behaviour above
// holds unchanged; only where the process runs, and the DSN's host, differ.
// Also measured against a real internal:true network: a host pgbench cannot
// even resolve the DSN's host ("could not translate host name"), while the
// namespace-joined one initializes and sustains ~1000 tps against the same
// address.
package run

import (
	"bytes"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// pgbenchInstallHint follows R-CLI-5's shape: state how to get the tool,
// never guess where it lives.
const pgbenchInstallHint = "install: pgbench ships with PostgreSQL's client tools (Debian/Ubuntu: apt install postgresql-client; macOS: brew install libpq) — https://www.postgresql.org/docs/current/pgbench.html, or install Docker, which lets TortureU run pgbench's own official image instead"

// pgbenchImage carries pgbench in container mode. The official postgres
// image ships it at /usr/local/bin/pgbench (verified), so no separate image
// is needed. Pinned rather than floating for the reason TBD-11 states about
// every other pin in this project: a tool that updates itself under a run
// makes every regression ambiguous.
const pgbenchImage = "postgres:16-alpine"

// PgbenchRunner drives pgbench. Its defaults are stated here rather than
// left to the caller because SPEC.md does not name them and they must not
// vary between runs: scale 1, 8 clients, 2 jobs — enough concurrency to
// saturate a local container-hosted database without turning the load
// generator itself into the bottleneck.
type PgbenchRunner struct {
	// Bin is the pgbench binary; defaults to "pgbench".
	Bin string
	// Clients/Jobs/Scale override pgbench's -c/-j/-s.
	Clients int
	Jobs    int
	Scale   int
	// Image is the container-mode image; defaults to pgbenchImage.
	Image string
	// DockerBin is the docker binary for container mode; defaults to
	// "docker".
	DockerBin string
	// Netns is the container whose network namespace pgbench joins when the
	// DSN's own address is not reachable from this host (R-EXE-26's Reach
	// rule, TBD-12). Empty means host-process mode only, which is what
	// every caller that never calls InNamespaceOf gets.
	Netns string
}

// InNamespaceOf returns a copy of r bound to container's network namespace.
// A copy, on a value receiver, deliberately: Deps wires PgbenchRunner as a
// value (deps.go), so there is nothing to mutate — and one run binding a
// namespace must not change what a later one does.
func (r PgbenchRunner) InNamespaceOf(container string) DBLoadRunner {
	r.Netns = container
	return r
}

func (r PgbenchRunner) image() string {
	if r.Image != "" {
		return r.Image
	}
	return pgbenchImage
}

func (r PgbenchRunner) dockerBin() string {
	if r.DockerBin != "" {
		return r.DockerBin
	}
	return "docker"
}

func (r PgbenchRunner) bin() string {
	if r.Bin != "" {
		return r.Bin
	}
	return "pgbench"
}

func (r PgbenchRunner) clients() int {
	if r.Clients > 0 {
		return r.Clients
	}
	return 8
}

func (r PgbenchRunner) jobs() int {
	if r.Jobs > 0 {
		return r.Jobs
	}
	return 2
}

func (r PgbenchRunner) scale() int {
	if r.Scale > 0 {
		return r.Scale
	}
	return 1
}

// Preflight reports a missing pgbench with an install hint (R-CLI-5),
// before the run resets anything (R-EXE-26). Two routes satisfy it, because
// two routes actually work: pgbench on PATH, or Docker, which lets this run
// pgbench's own official image in the database's network namespace — the
// only route that reaches a database R-DC2-3 isolated. Refusing a machine
// that has Docker but no postgresql-client would refuse the topology this
// project itself creates.
func (r PgbenchRunner) Preflight() error {
	if _, err := exec.LookPath(r.bin()); err == nil {
		return nil
	}
	if _, err := exec.LookPath(r.dockerBin()); err == nil {
		return nil
	}
	return fmt.Errorf("--db-load needs pgbench and %q is not on PATH, and neither is %q (which would let TortureU run pgbench's own container image instead) — %s", r.bin(), r.dockerBin(), pgbenchInstallHint)
}

// useContainer decides between the host process and a namespace-joined
// container, and does it by *asking* rather than assuming (R-CLI-6): if the
// caller's own address answers a TCP handshake from here, the host process
// can reach it and nothing else is needed. Only a target this host cannot
// reach — the internal:true case — falls back, and only when there is a
// namespace to fall back into and a DSN this package can rewrite exactly.
func (r PgbenchRunner) useContainer(dsn string) bool {
	if r.Netns == "" {
		return false
	}
	if _, ok := dsnInNamespace(dsn); !ok {
		// Not a form R-EXE-26's Reach rule can rewrite: the host process
		// runs and fails in the caller's own terms, which is the loud
		// failure R-EXE-26 requires over a guess.
		return false
	}
	if _, err := exec.LookPath(r.bin()); err != nil {
		return true
	}
	addr, ok := dsnAddr(dsn)
	if !ok {
		return false
	}
	return !hostCanReach(addr)
}

// Start initializes pgbench's tables and begins the load. Initialization
// creates and drops tables named pgbench_* in the target database; that is
// stated in `-db-load`'s own help text (R-EXE-26) because it is a write
// against the caller's data.
func (r PgbenchRunner) Start(dsn string, max time.Duration) (DBLoadHandle, error) {
	if r.useContainer(dsn) {
		return r.startContainer(dsn, max)
	}
	return r.startHostProcess(dsn, max)
}

// loadArgs are pgbench's own load-phase arguments, identical in both modes
// — the CLI contract every measured behaviour in this file's doc comment
// was measured against.
func (r PgbenchRunner) loadArgs(dsn string, max time.Duration) []string {
	seconds := int(max.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	return []string{
		"-c", strconv.Itoa(r.clients()),
		"-j", strconv.Itoa(r.jobs()),
		"-T", strconv.Itoa(seconds),
		"-P", "5",
		dsn,
	}
}

func (r PgbenchRunner) initArgs(dsn string) []string {
	return []string{"-i", "-q", "-s", strconv.Itoa(r.scale()), dsn}
}

func (r PgbenchRunner) startHostProcess(dsn string, max time.Duration) (DBLoadHandle, error) {
	init := exec.Command(r.bin(), r.initArgs(dsn)...)
	if out, err := init.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("pgbench -i (initialize pgbench_* tables): %w: %s", err, strings.TrimSpace(lastLines(string(out), 3)))
	}

	cmd := exec.Command(r.bin(), r.loadArgs(dsn, max)...)
	return r.startCmd(cmd, nil)
}

// startContainer runs the same pgbench CLI inside the official postgres
// image, joined to r.Netns's network namespace — the mechanism K6Runner
// already uses to reach an internal-only SUT (load.go), applied to the
// database. The DSN's host becomes that namespace's loopback and nothing
// else about it changes (dsnInNamespace).
//
// The container is named so Stop can remove it directly: killing the
// `docker run` client does not stop the container it started, and a
// pgbench that outlived the run would be exactly the "crashed run left
// pgbench hammering a developer's database" R-EXE-26 forbids.
func (r PgbenchRunner) startContainer(dsn string, max time.Duration) (DBLoadHandle, error) {
	local, ok := dsnInNamespace(dsn)
	if !ok {
		return nil, fmt.Errorf("pgbench: -db-url %q is not a conninfo URI, so its address cannot be rewritten into %s's network namespace — pass a postgres:// URI (R-EXE-26)", dsn, r.Netns)
	}

	initArgs := append([]string{"run", "--rm", "--network", "container:" + r.Netns,
		"--entrypoint", "pgbench", r.image()}, r.initArgs(local)...)
	if out, err := exec.Command(r.dockerBin(), initArgs...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("pgbench -i (initialize pgbench_* tables) inside %s's network namespace: %w: %s", r.Netns, err, strings.TrimSpace(lastLines(string(out), 3)))
	}

	name := fmt.Sprintf("tortureu-pgbench-%d", time.Now().UnixNano())
	runArgs := append([]string{"run", "--rm", "--name", name, "--network", "container:" + r.Netns,
		"--entrypoint", "pgbench", r.image()}, r.loadArgs(local, max)...)
	cmd := exec.Command(r.dockerBin(), runArgs...)
	return r.startCmd(cmd, func() {
		_ = exec.Command(r.dockerBin(), "rm", "-f", name).Run()
	})
}

// startCmd is the half both modes share: launch, stream pgbench's own
// output into a buffer the progress-line parser reads, and reap. alsoKill,
// when non-nil, is what container mode needs beyond killing the process —
// removing the container the process merely attached to.
func (r PgbenchRunner) startCmd(cmd *exec.Cmd, alsoKill func()) (DBLoadHandle, error) {
	buf := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = buf, buf
	if err := cmd.Start(); err != nil {
		if alsoKill != nil {
			alsoKill()
		}
		return nil, fmt.Errorf("pgbench: %w", err)
	}

	h := &pgbenchHandle{
		cmd:      cmd,
		out:      buf,
		clients:  r.clients(),
		started:  time.Now(),
		exited:   make(chan struct{}),
		alsoKill: alsoKill,
	}
	go func() {
		h.waitErr = cmd.Wait()
		close(h.exited)
	}()
	return h, nil
}

// pgbenchHandle is one running pgbench process.
type pgbenchHandle struct {
	cmd     *exec.Cmd
	out     *syncBuffer
	clients int
	started time.Time
	// alsoKill removes the container in container mode; nil for a host
	// process, whose own kill is the whole story.
	alsoKill func()

	exited  chan struct{}
	waitErr error

	once   sync.Once
	result DBLoadResult
}

// Stop ends the DB load. Idempotent, because teardownAll can reach it from
// several exit paths (R-EXE-5).
func (h *pgbenchHandle) Stop() DBLoadResult {
	h.once.Do(func() {
		cutShort := false
		select {
		case <-h.exited:
		default:
			cutShort = true
			_ = h.cmd.Process.Kill()
			if h.alsoKill != nil {
				h.alsoKill()
			}
			<-h.exited
		}
		// Unconditional in container mode, not only on the cut-short path:
		// `--rm` covers a container that exited on its own, but a run that
		// ended for any other reason must still leave nothing behind
		// (R-EXE-5). Removing an already-removed container is a no-op.
		if h.alsoKill != nil {
			h.alsoKill()
		}
		h.result = parsePgbenchOutput(h.out.String())
		h.result.Clients = h.clients
		h.result.CutShort = cutShort
		if h.result.DurationS == 0 {
			h.result.DurationS = time.Since(h.started).Seconds()
		}
		// A process we killed ourselves reports a signal exit status; that
		// is the run ending, not pgbench failing, so it is not an error.
		// An exit status pgbench chose itself is a real tool failure
		// (R-VER-2: status error, never a SUT failure).
		if !cutShort && h.waitErr != nil {
			h.result.Err = fmt.Errorf("pgbench: %w: %s", h.waitErr, strings.TrimSpace(lastLines(h.out.String(), 3)))
		}
	})
	return h.result
}

var (
	pgbenchTPSRe         = regexp.MustCompile(`(?m)^tps = ([0-9.]+)`)
	pgbenchTxnsRe        = regexp.MustCompile(`(?m)^number of transactions actually processed: (\d+)`)
	pgbenchProgressTPSRe = regexp.MustCompile(`progress: [0-9.]+ s, ([0-9.]+) tps`)
	pgbenchDurationRe    = regexp.MustCompile(`(?m)^duration: (\d+) s`)
)

// parsePgbenchOutput reads what pgbench achieved from its own output. Both
// shapes are handled deliberately: the final summary when it ran to
// completion, and the last `-P 5` progress line when the run cut it short
// (measured: SIGINT prints no summary at all).
func parsePgbenchOutput(out string) DBLoadResult {
	var res DBLoadResult
	if m := pgbenchTPSRe.FindStringSubmatch(out); m != nil {
		res.TPS, _ = strconv.ParseFloat(m[1], 64)
	} else if m := pgbenchProgressTPSRe.FindAllStringSubmatch(out, -1); len(m) > 0 {
		res.TPS, _ = strconv.ParseFloat(m[len(m)-1][1], 64)
	}
	if m := pgbenchTxnsRe.FindStringSubmatch(out); m != nil {
		res.Transactions, _ = strconv.Atoi(m[1])
	}
	// The declared -T window is the right duration for a run that ended by
	// itself; a run the orchestrator cut short has no summary at all
	// (measured), and Stop falls back to the wall-clock time it actually
	// ran for.
	if m := pgbenchDurationRe.FindStringSubmatch(out); m != nil {
		res.DurationS, _ = strconv.ParseFloat(m[1], 64)
	}
	return res
}

// lastLines returns the last n non-empty lines of s, for error messages
// that quote a tool's own reason without reproducing its whole output.
func lastLines(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}

// syncBuffer is a bytes.Buffer safe for the exec package's writer goroutine
// and this package's reader to share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
