package main

import "testing"

// spec: R-CLI-1
//
// Every verb that started as a stub (PLAN.md Task 8) has since graduated:
// mcp (Task 8 addendum, see mcp_test.go), check (R-CLI-7, see
// check_test.go), capture/replay (R-CLI-9/R-CLI-10, see
// capture_test.go/replay_test.go), and emit (R-CLI-8 proposed, see
// emit_test.go). stubVerbs is empty as a result; this test pins that down
// so a future stub verb added without a corresponding graduation test
// doesn't slip past unnoticed.
func TestStubVerbsMapIsEmpty(t *testing.T) {
	if len(stubVerbs) != 0 {
		t.Errorf("stubVerbs = %v, want empty — every entry should have its own graduation test instead", stubVerbs)
	}
}
