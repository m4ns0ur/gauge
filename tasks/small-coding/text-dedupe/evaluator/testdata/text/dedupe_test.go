package text

import (
	"reflect"
	"testing"
)

func TestDedupeStable(t *testing.T) {
	got := Dedupe([]string{"a", "b", "a", "c", "b", "a"})
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Dedupe = %v, want %v", got, want)
	}
	caseSensitive := Dedupe([]string{"A", "a", "A"})
	if !reflect.DeepEqual(caseSensitive, []string{"A", "a"}) {
		t.Errorf("Dedupe is not case-sensitive: %v", caseSensitive)
	}
	empty := Dedupe(nil)
	if empty == nil {
		t.Fatal("Dedupe(nil) returned a nil slice")
	}
	if len(empty) != 0 {
		t.Errorf("Dedupe(nil) = %v, want an empty slice", empty)
	}
}
