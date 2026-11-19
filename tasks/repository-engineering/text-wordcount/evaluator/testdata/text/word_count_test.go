package text

import "testing"

func TestWordCountPunctuationAndCase(t *testing.T) {
	counts := WordCount("Hello, hello!\nGrüße grüße; 42 42\ttabs")
	want := map[string]int{
		"hello":  2,
		"grüße":  2,
		"42":     2,
		"tabs":   1,
	}
	if len(counts) != len(want) {
		t.Fatalf("WordCount returned %d distinct words, want %d: %v", len(counts), len(want), counts)
	}
	for word, n := range want {
		if counts[word] != n {
			t.Errorf("count[%q] = %d, want %d (all counts: %v)", word, counts[word], n, counts)
		}
	}
}
