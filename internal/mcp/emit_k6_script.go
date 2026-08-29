package mcp

import (
	"github.com/jd316/TortureU/internal/config"
	"github.com/jd316/TortureU/internal/k6"
)

// EmitK6Script is the DC-1 escape hatch (R-DC1-2): it returns the compiled
// k6 script for cfg, verbatim from internal/k6.Compile, so an agent can
// extend it with k6's own tools once torture.yaml can no longer express
// what's needed. It performs no execution — it never starts k6, and no
// other function in this package calls it on EmitK6Script's behalf.
func EmitK6Script(cfg *config.Config) (string, error) {
	return k6.Compile(cfg)
}
