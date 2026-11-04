package gui

import "testing"

func TestGroups(t *testing.T) {
	cases := []struct {
		file, prefix string
		want         bool
	}{
		{"Pack/Voice.USen.pack::Voice/USen/BtlVoice.bars#A", "Pack/Voice.USen.pack", true},
		{"Pack/Voice.USen.pack::Voice/USen/BtlVoice.bars#A", "Pack/Voice.USen.pack::Voice/USen/BtlVoice.bars", true},
		{"Pack/Voice.USen.pack::Voice/USen/Training.bars#A", "Pack/Voice.USen.pack::Voice/USen/BtlVoice.bars", false},
		{"Sound/Resource/Stream/BGM.bwav", "Sound/Resource/Stream", true},
		{"Sound/Resource/BgmRhythmGame.bars#X", "Sound/Resource", false},
		{"Sound/Resource/BgmRhythmGame.bars#X", "Sound/Resource/BgmRhythmGame.bars", true},
		{"Pack/Voice.USen.pack::Voice/USen/BtlVoice.bars#A", "Pack", false},
	}
	for _, c := range cases {
		if got := inGroup(c.file, c.prefix); got != c.want {
			t.Errorf("inGroup(%q, %q) = %v", c.file, c.prefix, got)
		}
	}
	if g := groupKeys("Pack/Voice.USen.pack::Voice/USen/BtlVoice.bars#A"); len(g) != 2 {
		t.Errorf("группы: %v", g)
	}
	if l := groupLabel("Pack/Voice.USen.pack::Voice/USen/BtlVoice.bars"); l != "Voice.USen.pack › BtlVoice.bars" {
		t.Errorf("подпись: %q", l)
	}
}
