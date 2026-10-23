package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/qeinstein/paradox/version"
)

func versionCommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	info := version.Current()
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(info); err != nil {
			fmt.Fprintf(stderr, "version: %v\n", err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "PARADOX %s\n", info.String())
	fmt.Fprintf(stdout, "  go:   %s\n  os:   %s/%s\n", info.GoVersion, info.GOOS, info.GOARCH)
	return 0
}
