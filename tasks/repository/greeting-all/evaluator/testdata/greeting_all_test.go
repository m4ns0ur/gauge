package greeting

import "testing"

func TestGreetAllOrder(t *testing.T) {
	got := GreetAll([]string{"Ada", "Grace", "Linus"})
	want := []string{"Hello, Ada!", "Hello, Grace!", "Hello, Linus!"}
	if len(got) != len(want) {
		t.Fatalf("GreetAll returned %d greetings, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("GreetAll()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestGreetAllEmpty(t *testing.T) {
	got := GreetAll(nil)
	if got == nil {
		t.Fatal("GreetAll(nil) returned a nil slice, want an empty, non-nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("GreetAll(nil) returned %v, want an empty slice", got)
	}
}

func TestGreetBackwardCompatible(t *testing.T) {
	if got, want := Greet("World"), "Hello, World!"; got != want {
		t.Fatalf("Greet(%q) = %q, want %q", "World", got, want)
	}
	if got, want := Greet(""), "Hello, !"; got != want {
		t.Fatalf("Greet(%q) = %q, want %q", "", got, want)
	}
}
