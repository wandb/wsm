package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"
	"github.com/wandb/wsm/pkg/kubectl"
)

var kubeContext string

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "wsm",
	Short: "Weights & Biases Server Manager",
	Long:  `A utility for managing Weights & Biases Server deployments`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		if kubeContext != "" {
			kubectl.SetContext(kubeContext)
		}
	},
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
	rootCmd.PersistentFlags().StringVar(&kubeContext, "context", "", "name of the kubeconfig context to use")
}

func main() {
	Execute()
}
