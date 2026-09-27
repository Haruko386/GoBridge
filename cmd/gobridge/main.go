package main

import (
	"os"

	"github.com/Haruko386/GoBridge/internal/cli"
)

var version = "dev"

func main() {
	exitCode := cli.Run(os.Args[1:], os.Stdout, os.Stderr, version)
	os.Exit(exitCode)
}
