package text

import "testing"

func TestIsPalindrome(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"A man, a plan, a canal: Panama", true},
		{"No 'x' in Nixon", true},
		{"hello", false},
		{"12321", true},
		{"été", true},
		{"", true},
		{"...", true},
		{"ab", false},
	}
	for _, tc := range cases {
		if got := IsPalindrome(tc.input); got != tc.want {
			t.Errorf("IsPalindrome(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}
