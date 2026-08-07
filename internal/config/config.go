// Package config parses and validates torture.yaml (SPEC.md §4).
package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Config is the parsed and validated torture.yaml.
type Config struct {
	Version int
}

// topLevelKeys are the blocks R-CFG-2 allows at the top of torture.yaml.
var topLevelKeys = map[string]bool{
	"version": true,
	"target":  true,
	"egress":  true,
	"reset":   true,
	"load":    true,
	"faults":  true,
	"assert":  true,
	"fuzz":    true,
}

// Parse parses and validates raw torture.yaml bytes.
func Parse(raw []byte) (*Config, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 {
		return nil, fmt.Errorf("torture.yaml: empty document")
	}
	doc := root.Content[0]
	for i := 0; i < len(doc.Content); i += 2 {
		key := doc.Content[i].Value
		if !topLevelKeys[key] {
			return nil, fmt.Errorf("torture.yaml: unknown top-level key %q (line %d)", key, doc.Content[i].Line)
		}
	}
	return &Config{}, nil
}
