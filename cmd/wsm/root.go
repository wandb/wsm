package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use: "wsm",
	// Runtime (RunE) errors shouldn't dump the whole flags usage block; the error message stands alone.
	SilenceUsage: true,
	Short:        "Weights & Biases Server Manager",
	Long: `A utility for managing Weights & Biases Server deployments instances on Kubernetes — for local development and airgapped installs.
	
	  Quick start (local kind cluster):
		wsm deploy-v2 operator \
			--context kind-wandb \
			--setup-k8s-cluster \
			--cluster-name wandb \
			--include-cr
	  
	  Common commands:
		wsm deploy-v2 operator   Deploy the v2 operator (and optionally a W&B CR).
		wsm upgrade              Bump the W&B version on an existing install.
		wsm cluster cleanup      Remove everything wsm deployed.
		wsm list                 List the container images required for a deploy.
	
	  Run 'wsm <command> --help' for details on any command.`,
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

func init() {}

func main() {
	Execute()
}
