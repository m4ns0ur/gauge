package text

import "testing"

func TestTruncateRunes(t *testing.T) {
	cases := []struct {
		input string
		n     int
		want  string
	}{
		{"hello", 5, "hello"},
		{"hello", 3, "he…"},
		{"héllo", 3, "hé…"},
		{"hello", 1, "…"},
		{"hello", 0, ""},
		{"hello", -1, ""},
		{"hi", 10, "hi"},
	}
	for _, tc := range cases {
		if got := Truncate(tc.input, tc.n); got != tc.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tc.input, tc.n, got, tc.want)
		}
	}
}
