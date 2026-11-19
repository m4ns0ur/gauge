package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"example.com/notes/notes"
)

func main() {
	os.Exit(run(os.Args[1:], notes.NewStore(), os.Stdout, os.Stderr))
}

// run executes one CLI invocation. It is separate from main so tests can drive
// it with a store and captured streams.
func run(args []string, store *notes.Store, stdout, stderr io.Writer) int {
	usage := "usage: notes <add|list> [arguments]\n"
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "add":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "notes: add requires text")
			return 2
		}
		note, err := store.Add(strings.Join(args[1:], " "), nil)
		if err != nil {
			fmt.Fprintf(stderr, "notes: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "added %s\n", note.ID)
		return 0
	case "list":
		for _, note := range store.List() {
			fmt.Fprintf(stdout, "%s\t%s\n", note.ID, note.Text)
		}
		return 0
	default:
		// TODO: unknown commands and --help are not handled yet.
		return 0
	}
}
