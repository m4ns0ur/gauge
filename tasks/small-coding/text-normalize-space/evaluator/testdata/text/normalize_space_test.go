package text

import "testing"

func TestNormalizeSpace(t *testing.T) {
	cases := map[string]string{
		"  a  b\t\nc  ": "a b c",
		"":                "",
		"   \t\n ":       "",
		"already fine":     "already fine",
		"one\ttwo":        "one two",
	}
	for input, want := range cases {
		if got := NormalizeSpace(input); got != want {
			t.Errorf("NormalizeSpace(%q) = %q, want %q", input, got, want)
		}
	}
}
