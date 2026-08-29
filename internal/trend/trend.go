// Package trend is the local verdict store for cross-commit trend tracking
// (SPEC.md §7, R-CLI-14..R-CLI-17; resolves TBD-1).
//
// The store is JSONL: one JSON object per line, append-only. That choice is
// argued in full in SPEC.md §12's TBD-1 resolution; the short version is that
// a trend is only worth keeping if it can be committed, and an append-only
// text file diffs as the one line it gained while a SQLite file is opaque
// binary churn that two branches cannot merge. The row count is one per run,
// so nothing here needs an index.
//
// What is stored is a PROJECTION of the verdict (R-CLI-15), not the verdict:
// the identity of the run, its anchor, its outcome, the numeric leaves of its
// metrics, and the keys of its findings. That is exactly what a trend joins
// on. The verdict document itself remains the record of what happened.
//
// The anchor rule (R-CLI-17) is the one that keeps this honest: a run made
// outside a git checkout has an empty commit (R-VER-12), and an empty commit
// is not a point on a timeline. Such a row is kept — the run really happened
// — but never joined, and always reported as excluded.
package trend

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jd316/tortureu/internal/verdict"
)

// SchemaVersion is the version stamped on every record written by this build
// (R-CLI-15). A reader that meets a higher version reports and skips the line
// rather than guessing at fields it does not know.
const SchemaVersion = 1

// DefaultStore is where the store lives when -store is not given.
const DefaultStore = ".tortureu/trend.jsonl"

// Record is one run's projection into the store (R-CLI-15).
//
// Field names are stable: this file is committed to users' repos, so renaming
// a key would orphan every row written before the rename.
type Record struct {
	V         int                `json:"v"`
	RunID     string             `json:"run_id"`
	Scenario  string             `json:"scenario"`
	Commit    string             `json:"commit"`
	Status    string             `json:"status"`
	ExitCode  int                `json:"exit_code"`
	StartedAt string             `json:"started_at"`
	DurationS int                `json:"duration_s"`
	Metrics   map[string]float64 `json:"metrics,omitempty"`
	Findings  []string           `json:"findings,omitempty"`
}

// Anchored reports whether this record can join a series. R-VER-12 leaves
// Commit empty outside a git checkout, and R-CLI-17 forbids joining on that:
// every anchorless run would otherwise collapse onto one point.
func (r Record) Anchored() bool { return r.Commit != "" }

// Comparable reports whether this record's metrics are a measurement of the
// system under test. status:error means TortureU broke and status:aborted
// means the run never started (R-VER-2) — neither measured anything, so
// neither may contribute a delta (R-CLI-17).
func (r Record) Comparable() bool {
	return r.Status == string(verdict.StatusPass) || r.Status == string(verdict.StatusFail)
}

// Project turns a verdict document into the row the store keeps (R-CLI-15).
func Project(v verdict.Verdict) Record {
	r := Record{
		V:         SchemaVersion,
		RunID:     v.RunID,
		Scenario:  v.Scenario,
		Commit:    v.Commit,
		Status:    string(v.Status),
		ExitCode:  verdict.ExitCode(v),
		StartedAt: v.StartedAt,
		DurationS: v.DurationS,
		Metrics:   map[string]float64{},
	}
	flatten("", v.Metrics, r.Metrics)
	if len(r.Metrics) == 0 {
		r.Metrics = nil
	}
	for _, f := range v.Findings {
		r.Findings = append(r.Findings, FindingKey(f))
	}
	sort.Strings(r.Findings)
	return r
}

// FindingKey is a finding's identity ACROSS runs (R-CLI-14): the assertion it
// broke plus the fault named as its cause. Deliberately not the verdict's
// `id`, which is a position in one run's worst-first list — comparing
// positions reports a change whenever the ordering moved, and reports none
// when the first finding was replaced by a different one.
func FindingKey(f verdict.Finding) string {
	key := strings.TrimSpace(f.Broke.Assertion)
	if key == "" {
		key = "(no assertion)"
	}
	if f.Cause != nil && f.Cause.Fault != "" {
		key += "  [fault: " + f.Cause.Fault + "]"
	}
	if f.Unevaluated {
		key += "  (unevaluated)"
	}
	return key
}

// flatten walks a verdict's metrics map and records every numeric leaf under
// its dotted path. Non-numeric leaves (k6's threshold booleans, strings) are
// dropped: a trend is a series of numbers, and there is nothing to subtract
// from "true".
func flatten(prefix string, in map[string]any, out map[string]float64) {
	for k, val := range in {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch t := val.(type) {
		case map[string]any:
			flatten(key, t, out)
		case float64:
			out[key] = t
		case float32:
			out[key] = float64(t)
		case int:
			out[key] = float64(t)
		case int64:
			out[key] = float64(t)
		case json.Number:
			if f, err := t.Float64(); err == nil {
				out[key] = f
			}
		}
	}
}

// Append writes one record as one whole line, under an advisory exclusive
// lock (R-CLI-16). Two CI jobs finishing at the same instant therefore
// produce two complete lines in some order, never one interleaved line that
// parses as neither run.
//
// Nothing here rewrites a byte it did not just append. That is deliberate: a
// writer that can also modify history turns a torn write into unrecoverable
// loss instead of one line a reader can skip.
func Append(path string, rec Record) error {
	if path == "" {
		path = DefaultStore
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("trend: create store directory: %w", err)
		}
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("trend: encode record: %w", err)
	}
	line = append(line, '\n')

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("trend: open store: %w", err)
	}
	defer f.Close()
	unlock, err := lockExclusive(f)
	if err != nil {
		return fmt.Errorf("trend: lock store: %w", err)
	}
	defer unlock()
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("trend: append: %w", err)
	}
	return f.Sync()
}

// Skip is one line the reader could not use, named so it is visible rather
// than silently absent (R-CLI-15, R-COV-6).
type Skip struct {
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

// Store is a loaded trend file: the records it could read, and a note for
// every line it could not.
type Store struct {
	Path    string
	Records []Record
	Skipped []Skip
}

// Load reads a store. A store that does not exist yet is an empty trend, not
// an error — the first `trend record` creates it.
func Load(path string) (*Store, error) {
	if path == "" {
		path = DefaultStore
	}
	s := &Store{Path: path}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("trend: open store: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// A record is small, but a scenario name is user-supplied; give the
	// scanner room so a long line is read rather than reported as corrupt.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			s.Skipped = append(s.Skipped, Skip{Line: n, Reason: "not valid JSON: " + err.Error()})
			continue
		}
		if rec.V != SchemaVersion {
			s.Skipped = append(s.Skipped, Skip{Line: n, Reason: fmt.Sprintf(
				"schema version %d, this build understands %d", rec.V, SchemaVersion)})
			continue
		}
		s.Records = append(s.Records, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("trend: read store: %w", err)
	}
	return s, nil
}

// Filter narrows what Report returns.
type Filter struct {
	Scenario string // only this scenario's series
	Metric   string // only metric keys containing this substring
	Last     int    // keep only the last N rows (0 = all)
}

// Row is one anchored run in the report, with what changed since the previous
// comparable run of the same scenario.
type Row struct {
	Record
	Comparable   bool
	Deltas       map[string]float64
	NewFindings  []string
	GoneFindings []string
}

// Report is what `tortureu trend show` renders.
type Report struct {
	Path       string
	Rows       []Row
	Unanchored int
	Skipped    []Skip
	Metrics    []string // every metric key present, sorted
	Filter     Filter
}

// Report builds the series (R-CLI-14). Rows keep the order they were
// appended in: that is the order the runs were recorded, which needs no trust
// in any machine's clock. Comparison is per scenario — two scenarios are two
// different measurements and share no baseline.
func (s *Store) Report(f Filter) Report {
	rep := Report{Path: s.Path, Skipped: s.Skipped, Filter: f}
	prev := map[string]Record{}       // scenario -> last comparable record
	prevFind := map[string][]string{} // scenario -> its finding keys
	seenMetric := map[string]bool{}

	for _, rec := range s.Records {
		if f.Scenario != "" && rec.Scenario != f.Scenario {
			continue
		}
		// R-CLI-17: an anchorless run is counted and named, never joined.
		if !rec.Anchored() {
			rep.Unanchored++
			continue
		}
		row := Row{Record: rec, Comparable: rec.Comparable()}
		if row.Comparable {
			row.Deltas = map[string]float64{}
			if base, ok := prev[rec.Scenario]; ok {
				for k, now := range rec.Metrics {
					if before, ok := base.Metrics[k]; ok {
						row.Deltas[k] = now - before
					}
				}
				row.NewFindings, row.GoneFindings = diffFindings(prevFind[rec.Scenario], rec.Findings)
			}
			prev[rec.Scenario] = rec
			prevFind[rec.Scenario] = rec.Findings
		}
		for k := range rec.Metrics {
			if f.Metric == "" || strings.Contains(k, f.Metric) {
				seenMetric[k] = true
			}
		}
		rep.Rows = append(rep.Rows, row)
	}

	if f.Last > 0 && len(rep.Rows) > f.Last {
		rep.Rows = rep.Rows[len(rep.Rows)-f.Last:]
	}
	for k := range seenMetric {
		rep.Metrics = append(rep.Metrics, k)
	}
	sort.Strings(rep.Metrics)
	return rep
}

// diffFindings reports what appeared and what went away between two runs.
func diffFindings(before, after []string) (added, gone []string) {
	was := map[string]bool{}
	for _, k := range before {
		was[k] = true
	}
	is := map[string]bool{}
	for _, k := range after {
		is[k] = true
		if !was[k] {
			added = append(added, k)
		}
	}
	for _, k := range before {
		if !is[k] {
			gone = append(gone, k)
		}
	}
	sort.Strings(added)
	sort.Strings(gone)
	return added, gone
}
