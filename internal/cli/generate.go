package cli

import (
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
)

func NewRootCommandForGeneration() *cobra.Command {
	app := &App{stdin: os.Stdin, stdout: io.Discard, stderr: io.Discard}
	app.configureLogger(0)
	return app.newRootCommand()
}

func GenerateMarkdownDocs(dir string) error {
	root := NewRootCommandForGeneration()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return doc.GenMarkdownTree(root, dir)
}
