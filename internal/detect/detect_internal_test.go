package detect

import "testing"

func TestPreferDigestTag(t *testing.T) {
	cases := []struct {
		digestTag, label string
		want             bool
	}{
		{"1.10.2", "znc-1.10.2-ls181", false}, // same core, name-prefixed label -> keep label
		{"1.10.2", "1.10.2-ls183", false},     // same core, suffixed label -> keep label
		{"15.1.2", "24.04", true},             // base-OS mislabel, different core -> override
		{"1.10.2", "", true},                  // empty label -> override
		{"1.10.2", "master-omnibus", true},    // unparseable label -> override
		{"1.10.2", "1.10.2", false},           // identical core -> keep label (no downgrade)
	}
	for _, c := range cases {
		if got := preferDigestTag(c.digestTag, c.label); got != c.want {
			t.Errorf("preferDigestTag(%q, %q) = %v, want %v", c.digestTag, c.label, got, c.want)
		}
	}
}
