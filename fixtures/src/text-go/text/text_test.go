package text

import "testing"

func TestReverseASCIIText(t *testing.T) {
	if got := Reverse("abc"); got != "cba" {
		t.Fatalf("Reverse(%q) = %q, want %q", "abc", got, "cba")
	}
}

func TestWordCountSpaces(t *testing.T) {
	counts := WordCount("hello hello world")
	if counts["hello"] != 2 {
		t.Errorf("hello count = %d, want 2", counts["hello"])
	}
	if counts["world"] != 1 {
		t.Errorf("world count = %d, want 1", counts["world"])
	}
}
