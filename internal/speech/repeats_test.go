package speech

import "testing"

func TestCleanTranscript(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Flutter kick Flutter kick Flutter kick Flutter kick Flutter", "Flutter kick"},
		{"Warrior One Pose Warrior One Pose Warrior One Pose Warrior One One Pose Warrior One One Po", "Warrior One Pose"},
		{"Squeeze your abdominal muscles and breathe deep. Breathe deep. Breathe deep. Breathe deep.", "Squeeze your abdominal muscles and breathe deep."},
		{"Yeah! Yeah! Yeah! Yeah! Yeah! Yeah!", "Yeah!"},
		// обычные повторы в речи остаются
		{"Thank you. Thank you.", "Thank you. Thank you."},
		{"There's more! There's more!", "There's more! There's more!"},
		{"No, no, no! Keep going.", "No, no, no! Keep going."},
		{"Left, right, left, right!", "Left, right, left, right!"},
	} {
		if got, _ := CleanTranscript(c.in); got != c.want {
			t.Errorf("%q → %q, ждали %q", c.in, got, c.want)
		}
	}
}

func TestCollapseDouble(t *testing.T) {
	for _, c := range []struct {
		in   string
		sec  float64
		want string
	}{
		{"Good effort. Good effort.", 0.6, "Good effort."},
		{"Doing great! Doing great!", 0.7, "Doing great!"},
		{"Pendulum bend. Pendulum bend.", 1.0, "Pendulum bend."},
		// настоящие повторы в обычном темпе остаются
		{"There's more! There's more!", 1.6, "There's more! There's more!"},
		{"Still coming! Still coming!", 2.0, "Still coming! Still coming!"},
		{"Keep going!", 0.5, "Keep going!"},
		{"Left, right, left, right!", 0.8, "Left, right, left, right!"},
	} {
		if got, _ := CollapseDouble(c.in, c.sec); got != c.want {
			t.Errorf("%q за %.1f с → %q, ждали %q", c.in, c.sec, got, c.want)
		}
	}
}
