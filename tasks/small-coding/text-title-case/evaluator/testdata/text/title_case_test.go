package text

import "testing"

func TestTitleCase(t *testing.T) {
	cases := map[string]string{
		"hello world":          "Hello World",
		"HELLO":                "Hello",
		"héllo wörld": "Héllo Wörld",
		"  multiple   spaces ": "Multiple Spaces",
		"already Title":        "Already Title",
		"":                     "",
		"\t\n ":               "",
	}
	for input, want := range cases {
		if got := TitleCase(input); got != want {
			t.Errorf("TitleCase(%q) = %q, want %q", input, got, want)
		}
	}
}
