package text

import "testing"

func TestReverseUnicode(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"ascii", "abc", "cba"},
		{"multibyte", "héllo", "olléh"},
		{"emoji", "a😀b", "b😀a"},
		{"japanese", "日本語", "語本日"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		if got := Reverse(tc.input); got != tc.want {
			t.Errorf("%s: Reverse(%q) = %q, want %q", tc.name, tc.input, got, tc.want)
		}
	}
}
