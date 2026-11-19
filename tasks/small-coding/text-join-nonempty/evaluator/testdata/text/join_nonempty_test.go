package text

import "testing"

func TestJoinNonEmpty(t *testing.T) {
	cases := []struct {
		parts []string
		sep   string
		want  string
	}{
		{[]string{"a", "", "b"}, "-", "a-b"},
		{[]string{" a ", " b "}, "/", "a/b"},
		{[]string{"", "  "}, ",", ""},
		{nil, ",", ""},
		{[]string{"only"}, ",", "only"},
		{[]string{" x ", "", "y", "  "}, " | ", "x | y"},
	}
	for _, tc := range cases {
		if got := JoinNonEmpty(tc.parts, tc.sep); got != tc.want {
			t.Errorf("JoinNonEmpty(%v, %q) = %q, want %q", tc.parts, tc.sep, got, tc.want)
		}
	}
}
