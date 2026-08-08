package verdict

import "testing"

// spec: R-VER-14
func TestConfidenceAtMostClampsToCeiling(t *testing.T) {
	cases := []struct {
		name    string
		c       Confidence
		ceiling Confidence
		want    Confidence
	}{
		{"caused under a caused ceiling stands", Caused, Caused, Caused},
		{"caused under a correlated ceiling is clamped", Caused, Correlated, Correlated},
		{"correlated under a caused ceiling is not promoted", Correlated, Caused, Correlated},
		{"ambiguous is never promoted", Ambiguous, Caused, Ambiguous},
		{"an unstated ceiling does not clamp", Caused, "", Caused},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.AtMost(tc.ceiling); got != tc.want {
				t.Errorf("%q.AtMost(%q) = %q, want %q", tc.c, tc.ceiling, got, tc.want)
			}
		})
	}
}
