package greeting

import "testing"

func TestGreet(t *testing.T) {
	if got, want := Greet("World"), "Hello, World!"; got != want {
		t.Fatalf("Greet(%q) = %q, want %q", "World", got, want)
	}
}

func TestGreetEmptyName(t *testing.T) {
	if got, want := Greet(""), "Hello, !"; got != want {
		t.Fatalf("Greet(%q) = %q, want %q", "", got, want)
	}
}
