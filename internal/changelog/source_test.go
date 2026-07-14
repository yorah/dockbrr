package changelog

import "testing"

func TestFirstNonEmpty(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"", "a", "b"}, "a"},
		{[]string{"x", "y"}, "x"},
		{[]string{"", ""}, ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := firstNonEmpty(c.in...); got != c.want {
			t.Errorf("firstNonEmpty(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
