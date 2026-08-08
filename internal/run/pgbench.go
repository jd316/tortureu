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
const pgbenchInstallHint = "install: pgbench ships with PostgreSQL's client tools (Debian/Ubuntu: apt install postgresql-client; macOS: brew install libpq) — https://www.postgresql.org/docs/current/pgbench.html"

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
// before the run resets anything (R-EXE-26).
func (r PgbenchRunner) Preflight() error {
	if _, err := exec.LookPath(r.bin()); err != nil {
		return fmt.Errorf("--db-load needs pgbench and %q is not on PATH — %s", r.bin(), pgbenchInstallHint)
	}
	return nil
}

// Start initializes pgbench's tables and begins the load. Initialization
// creates and drops tables named pgbench_* in the target database; that is
// stated in `-db-load`'s own help text (R-EXE-26) because it is a write
// against the caller's data.
func (r PgbenchRunner) Start(dsn string, max time.Duration) (DBLoadHandle, error) {
	init := exec.Command(r.bin(), "-i", "-q", "-s", strconv.Itoa(r.scale()), dsn)
	if out, err := init.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("pgbench -i (initialize pgbench_* tables): %w: %s", err, strings.TrimSpace(lastLines(string(out), 3)))
	}

	seconds := int(max.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	cmd := exec.Command(r.bin(),
		"-c", strconv.Itoa(r.clients()),
		"-j", strconv.Itoa(r.jobs()),
		"-T", strconv.Itoa(seconds),
		"-P", "5",
		dsn)
	buf := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = buf, buf
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("pgbench: %w", err)
	}

	h := &pgbenchHandle{
		cmd:     cmd,
		out:     buf,
		clients: r.clients(),
		started: time.Now(),
		exited:  make(chan struct{}),
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
			<-h.exited
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
