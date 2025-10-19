package main

import (
	"fmt"
	"os"

	"github.com/so2liu/imgcd/internal/cli"
)

// version is set at build time via ldflags
var version = "dev"

func main() {
	cli.Version = version
	if err := cli.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
