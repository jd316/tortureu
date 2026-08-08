package emit

import (
	"strings"
	"testing"
)

func TestEmit_UnknownToolListsSupported(t *testing.T) {
	cfg := mustParse(t, netemFixture)
	_, err := Emit("gatling", cfg)
	if err == nil {
		t.Fatal("expected an error for an unsupported tool")
	}
	for _, tool := range Tools {
		if !strings.Contains(err.Error(), tool) {
			t.Errorf("expected error to list %q, got: %v", tool, err)
		}
	}
}

func TestEmit_DispatchesToEachTool(t *testing.T) {
	cfg := mustParse(t, netemFixture)
	for _, tool := range Tools {
		if _, err := Emit(tool, cfg); err != nil {
			t.Errorf("Emit(%q): %v", tool, err)
		}
	}
}
