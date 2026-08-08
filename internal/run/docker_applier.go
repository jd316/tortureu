// DockerApplier is the real implementation of fault.Applier's ApplyDocker
// for container/cgroup-scoped faults (R-EXE-6: container scope only, never
// the host — every action below names a single container, never touches
// host tc/cgroups/processes). fault.Manager's Applier seam had only a fake
// implementation before this task (see internal/fault's package doc); this
// is the real one.
package run

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"

	"github.com/jdb316/tortureu/internal/fault"
)

// DockerApplier drives Docker via the CLI (no Docker SDK dependency —
// keeps this package's dependency surface to what go.mod already has).
type DockerApplier struct {
	// Bin is the docker binary; defaults to "docker".
	Bin string
}

func (a DockerApplier) bin() string {
	if a.Bin != "" {
		return a.Bin
	}
	return "docker"
}

func (a DockerApplier) run(args ...string) (string, error) {
	cmd := exec.Command(a.bin(), args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, out.String())
	}
	return out.String(), nil
}

// ApplyToxic is not implemented by DockerApplier: network faults are
// Toxiproxy's (R-EXE-15's ownership table). CombinedApplier (applier.go)
// always routes a KindToxic action to ToxiproxyApplier instead, so this
// only fires on a caller bug.
func (a DockerApplier) ApplyToxic(name string, t fault.Toxic) (func() error, error) {
	return nil, fmt.Errorf("run: DockerApplier does not apply network toxics (fault %q)", name)
}

// dockerSignal reads d.Args["signal"] — internal/fault's translateDocker
// sets it to "SIGSTOP"/"SIGKILL"/"SIGTERM" for pause/kill/graceful
// respectively — falling back to fallback if it is absent (an Action built
// directly rather than via fault.Translate, as this package's own
// pre-existing tests do). Reading it rather than hardcoding per Kind means
// this applier automatically follows internal/fault's table if it ever
// changes, and — the reason this exists — sends kill and graceful as two
// genuinely distinct signals (SIGKILL vs SIGTERM) rather than two Docker
// subcommands (`docker kill` vs `docker stop`) whose own escalation
// behavior (`docker stop` sends SIGTERM, then SIGKILL after a grace period
// if the process hasn't exited) could blur the two together: a B1 finding
// reported kill and graceful as client-indistinguishable, which
// internal/fault's owner confirmed is this applier's defect, not a spec
// error — SPEC.md's three-verb distinction is real and this must honor it.
func dockerSignal(d fault.DockerAction, fallback string) string {
	if sig, ok := d.Args["signal"].(string); ok && sig != "" {
		return sig
	}
	return fallback
}

// ApplyDocker performs d against its target container (R-EXE-6) and returns
// an undo. Supported kinds mirror internal/fault/translate.go's
// translateDocker: "stress", "cpu_limit", "mem_limit", "pause", "kill",
// "graceful".
func (a DockerApplier) ApplyDocker(name string, d fault.DockerAction) (func() error, error) {
	switch d.Kind {
	case "pause":
		// Unchanged: docker pause/unpause's cgroup freezer is what makes
		// .State.Paused observable and stops every process in the
		// container at once, not just PID 1 — a strictly stronger
		// implementation of R-CFG-14's pause: SIGSTOP-equivalent semantics
		// than sending a literal SIGSTOP would be (confirmed empirically:
		// `docker kill --signal SIGSTOP` does stop the process but never
		// sets .State.Paused, which would have silently broken this
		// package's own existing pause test).
		if _, err := a.run("pause", d.Container); err != nil {
			return nil, err
		}
		return func() error {
			_, err := a.run("unpause", d.Container)
			return err
		}, nil

	case "kill":
		sig := dockerSignal(d, "SIGKILL")
		if _, err := a.run("kill", "--signal", sig, d.Container); err != nil {
			return nil, err
		}
		return func() error {
			_, err := a.run("start", d.Container)
			return err
		}, nil

	case "graceful":
		// `docker kill --signal SIGTERM`, not `docker stop`: `docker stop`
		// sends SIGTERM but then escalates to SIGKILL itself after a grace
		// period if the process hasn't exited, which can make a
		// slow-to-shut-down "graceful" fault indistinguishable from "kill"
		// at the client's TCP connection — exactly the B1 finding this
		// fixes. Sending only the named signal, with no Docker-side
		// escalation, keeps the three fault classes actually distinct.
		sig := dockerSignal(d, "SIGTERM")
		if _, err := a.run("kill", "--signal", sig, d.Container); err != nil {
			return nil, err
		}
		return func() error {
			_, err := a.run("start", d.Container)
			return err
		}, nil

	case "cpu_limit":
		prev, err := a.inspect(d.Container, "{{.HostConfig.NanoCpus}}")
		if err != nil {
			return nil, err
		}
		if _, err := a.run("update", "--cpus", fmt.Sprint(d.Args["limit"]), d.Container); err != nil {
			return nil, err
		}
		return func() error {
			_, err := a.run("update", "--cpus", cpusFromNanoCPUs(prev), d.Container)
			return err
		}, nil

	case "mem_limit":
		prev, err := a.inspect(d.Container, "{{.HostConfig.Memory}}")
		if err != nil {
			return nil, err
		}
		if _, err := a.run("update", "--memory", fmt.Sprint(d.Args["limit"]), d.Container); err != nil {
			return nil, err
		}
		return func() error {
			_, err := a.run("update", "--memory", prev, d.Container)
			return err
		}, nil

	case "stress":
		resource := fmt.Sprint(d.Args["resource"])
		workers := "1"
		if w, ok := d.Args["workers"]; ok {
			workers = fmt.Sprint(w)
		}
		args := []string{"exec", "-d", d.Container, "stress-ng", "--" + resource, workers}
		// cpu_percent (int, 0-100, stress-ng's own --cpu-load semantics):
		// internal/fault sets this for the cpu verb specifically (a
		// distinct key from mem/io/fd's "amount", so this applier has one
		// unambiguous, unit-typed way to read it). Before this fix it was
		// computed and never read: `cpu: 90%` produced stress-ng's default
		// full-tilt load per worker regardless of the requested percentage
		// — B1 measured ~403-416% of one core for a 90% request. A
		// requested percentage the applier silently ignores is the worst
		// kind of fault: the run completes, the verdict reads normally,
		// and the load applied was ~4.5x what was asked for.
		if resource == "cpu" {
			if pct, ok := d.Args["cpu_percent"]; ok {
				args = append(args, "--cpu-load", fmt.Sprint(pct))
			}
		}
		if _, err := a.run(args...); err != nil {
			return nil, err
		}
		return func() error {
			_, err := a.run("exec", d.Container, "pkill", "stress-ng")
			return err
		}, nil

	default:
		return nil, fmt.Errorf("run: DockerApplier: unsupported action kind %q (fault %q)", d.Kind, name)
	}
}

func (a DockerApplier) inspect(container, format string) (string, error) {
	out, err := a.run("inspect", "-f", format, container)
	return strings.TrimSpace(out), err
}

// cpusFromNanoCPUs converts docker inspect's NanoCpus (integer, 1e9 per
// core) back into the fractional-core string `docker update --cpus` takes,
// so cpu_limit's undo restores the previous ceiling rather than clearing it
// to unlimited.
func cpusFromNanoCPUs(nanoCPUs string) string {
	if nanoCPUs == "" || nanoCPUs == "0" {
		return "0" // 0 means "no limit" to `docker update --cpus`
	}
	var n int64
	if _, err := fmt.Sscanf(nanoCPUs, "%d", &n); err != nil {
		return "0"
	}
	return fmt.Sprintf("%.2f", float64(n)/1e9)
}
