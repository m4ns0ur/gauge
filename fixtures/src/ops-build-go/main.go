package main

import "fmt"

func main() {
	fmt.Println(Greeting("world"))
}

// Greeting returns a greeting for name.
func Greeting(name string) string {
	return "Hello, " + renderName(name)
}

func displayName(name string) string {
	return name
}
