// Package tortureu holds only the go:embed directive for registry.yaml
// (R-COV-8): a static binary that depends on a file it does not ship is
// not one.
//
// This file lives at the repo root, not in internal/doctor, because
// go:embed can only reference files inside its own source file's directory
// tree — it cannot climb out via "..". registry.yaml stays at the repo
// root as the single editable source of truth (R-COV-1; check.py validates
// it there); this embeds that exact file rather than a copy, so there is
// nothing to drift.
package tortureu

import _ "embed"

//go:embed registry.yaml
var RegistryYAML []byte
