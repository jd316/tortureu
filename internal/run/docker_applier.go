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

// ApplyDocker performs d against its target container (R-EXE-6) and returns
// an undo. Supported kinds mirror internal/fault/translate.go's
// translateDocker: "stress", "cpu_limit", "mem_limit", "pause", "kill",
// "graceful".
func (a DockerApplier) ApplyDocker(name string, d fault.DockerAction) (func() error, error) {
	switch d.Kind {
	case "pause":
		if _, err := a.run("pause", d.Container); err != nil {
			return nil, err
		}
		return func() error {
			_, err := a.run("unpause", d.Container)
			return err
		}, nil

	case "kill":
		if _, err := a.run("kill", d.Container); err != nil {
			return nil, err
		}
		return func() error {
			_, err := a.run("start", d.Container)
			return err
		}, nil

	case "graceful":
		if _, err := a.run("stop", d.Container); err != nil {
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
		flag := "--" + resource
		if _, err := a.run("exec", "-d", d.Container, "stress-ng", flag, workers); err != nil {
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
