package main

import (
	"fmt"
	"io"
	"os"

	"github.com/marcel/wtree/internal/cli"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is deliberately limited to process-boundary concerns: execute the CLI,
// render an error diagnostic, and return the mapped exit code.
func run(args []string, stdout, stderr io.Writer) int {
	err := cli.Execute(args, stdout, stderr)
	if err == nil {
		return 0
	}
	if !cli.JSONRequested(args) {
		fmt.Fprintf(stderr, "wtree: %v\n", err)
	}
	return cli.ExitCode(err)
}
