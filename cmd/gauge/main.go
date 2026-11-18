// Command gauge runs agent-neutral AI benchmarks.
package main

import (
	"fmt"
	"os"

	"github.com/m4ns0ur/gauge/internal/cli"
)

func main() {
	app := cli.New()
	if err := app.Root().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "gauge: %v\n", err)
		os.Exit(cli.ExitCode(err))
	}
}
