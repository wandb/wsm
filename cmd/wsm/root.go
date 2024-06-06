package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "wsm",
	Short: "Weights & Biases Server Manager",
	Long:  `A utility for managing Weights & Biases Server deployments`,
}

// Child commands add themselves via init() so all that should be done here is to set global
// flags appropriately. This is called by main(). It only needs to happen once to the rootCmd.
func Execute() {
	ctx := context.Background()
	err := rootCmd.ExecuteContext(ctx)
	if err != nil {
		os.Exit(1)
	}
}

func init() {

}

func main() {
	Execute()
}
