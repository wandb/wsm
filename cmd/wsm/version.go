package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// These are populated at build time via -ldflags "-X main.version=... -X main.commit=... -X main.date=...".
// GoReleaser injects them by default; local `make build` stamps a dev value.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func init() {
	rootCmd.Version = version
	rootCmd.AddCommand(VersionCmd())
}

func VersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the wsm version, commit, and build date",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("wsm %s (commit %s, built %s)\n", version, commit, date)
		},
	}
}
