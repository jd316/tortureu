package emit

import (
	"strings"
	"testing"
)

func TestEmit_UnknownToolListsSupported(t *testing.T) {
	cfg := mustParse(t, netemFixture)
	_, err := Emit("not-a-real-tool", cfg, nil)
	if err == nil {
		t.Fatal("expected an error for an unsupported tool")
	}
	for _, tool := range Tools() {
		if !strings.Contains(err.Error(), tool) {
			t.Errorf("expected error to list %q, got: %v", tool, err)
		}
	}
}

// Every registered emitter must be reachable through Emit. Emitters that need
// a dependency address (sysbench, memtier, fio) legitimately fail on a nil
// *detect.System — what this asserts is dispatch, not success, so the failure
// must be the emitter's own and never "unknown tool".
func TestEmit_DispatchesToEveryRegisteredTool(t *testing.T) {
	cfg := mustParse(t, netemFixture)
	for _, tool := range Tools() {
		_, err := Emit(tool, cfg, nil)
		if err != nil && strings.Contains(err.Error(), "unknown tool") {
			t.Errorf("Emit(%q) reported unknown tool despite being registered", tool)
		}
	}
}
