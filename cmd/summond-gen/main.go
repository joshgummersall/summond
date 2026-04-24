package main

import (
	"fmt"
	"os"

	"github.com/standardlabs/summond/internal/cli"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: summond-gen <docs> <output-dir>")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "docs":
		err = cli.GenerateMarkdownDocs(os.Args[2])
	default:
		fmt.Fprintf(os.Stderr, "unknown generator %q\n", os.Args[1])
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
