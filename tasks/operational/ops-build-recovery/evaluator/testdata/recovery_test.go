package main

import "testing"

func TestGreetingRecovered(t *testing.T) {
	if got := Greeting("world"); got != "Hello, world" {
		t.Fatalf("Greeting(%q) = %q, want %q", "world", got, "Hello, world")
	}
	if got := Greeting("Gauge"); got != "Hello, Gauge" {
		t.Fatalf("Greeting(%q) = %q, want %q", "Gauge", got, "Hello, Gauge")
	}
}
