package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/Haruko386/GoBridge/internal/config"
)

const usage = `GoBridge - bind machines, not IP addresses.

Usage:
  gobridge <command> [options]

Commands:
  init       Initialize this machine
  help       Show help information
  version    Show version information
`

// Run executes the command-line interface and returns a process exit code.
func Run(args []string, stdout, stderr io.Writer, version string) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return 0
	}

	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	case "version", "-v", "--version":
		fmt.Fprintf(stdout, "gobridge %s\n", version)
		return 0
	case "init":
		return runInit(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		fmt.Fprint(stderr, usage)
		return 2
	}
}

func runInit(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)

	roleValue := flags.String("role", "", "machine role: server or client")
	configDir := flags.String("config-dir", "", "configuration directory (default: ~/.gobridge)")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: gobridge init --role <server|client> [--config-dir <path>]")
		flags.PrintDefaults()
	}

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "init: unexpected arguments: %v\n\n", flags.Args())
		flags.Usage()
		return 2
	}

	if *roleValue == "" {
		fmt.Fprintln(stderr, "init: --role is required")
		flags.Usage()
		return 2
	}

	role, err := config.ParseRole(*roleValue)
	if err != nil {
		fmt.Fprintf(stderr, "init: %v\n", err)
		return 2
	}

	if *configDir == "" {
		*configDir, err = config.DefaultDir()
		if err != nil {
			fmt.Fprintf(stderr, "init: %v\n", err)
			return 1
		}
	}

	cfg, err := config.New(role)
	if err != nil {
		fmt.Fprintf(stderr, "init: %v\n", err)
		return 1
	}

	if err := config.SaveNew(*configDir, cfg); err != nil {
		if errors.Is(err, config.ErrAlreadyInitialized) {
			fmt.Fprintf(stderr, "init: %v\n", err)
			return 1
		}

		fmt.Fprintf(stderr, "init: save configuration: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Initialized GoBridge %s at %s\n", role, config.FilePath(*configDir))

	return 0
}
