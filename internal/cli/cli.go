package cli

import (
	"fmt"
	"io"
)

const version = "0.1.0"

func Run(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		printUsage(stdout)
		return nil
	}

	switch args[0] {
	case "hello":
		name := "world"
		if len(args) > 1 {
			name = args[1]
		}
		_, err := fmt.Fprintf(stdout, "hello, %s\n", name)
		return err
	case "version":
		_, err := fmt.Fprintf(stdout, "summond %s\n", version)
		return err
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func printUsage(stdout io.Writer) {
	fmt.Fprintln(stdout, "summond")
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Usage:")
	fmt.Fprintln(stdout, "  summond <command> [arguments]")
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Commands:")
	fmt.Fprintln(stdout, "  hello [name]   Print a greeting")
	fmt.Fprintln(stdout, "  version        Print the CLI version")
	fmt.Fprintln(stdout, "  help           Show this message")
}
