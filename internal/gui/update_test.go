package gui

import "testing"

func TestNewerVersion(t *testing.T) {
	for _, c := range []struct {
		cur, latest string
		want        bool
	}{{"1.0.0", "v1.0.1", true}, {"1.0.0", "1.0.0", false}, {"1.2.0", "1.10.0", true}, {"2.0.0", "1.9.9", false},
		{"1.0.0", "1.0", false}, {"1.0", "1.0.1", true}, {"1.0.0", "junk", false}} {
		if got := newerVersion(c.cur, c.latest); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v", c.cur, c.latest, got)
		}
	}
}
