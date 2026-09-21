package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
	"github.com/wandb/wsm/pkg/deployer"
)

func init() {
	rootCmd.AddCommand(MigrateCmd())
}

func MigrateCmd() *cobra.Command {
	var namespace string

	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Migrate existing W&B deployment to operator-based deployment",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("Starting migration to operator-based deployment...")

			// Fetch the latest tag for the controller
			latestTag, err := getLatestWandbTag()
			if err != nil {
				fmt.Printf("Error fetching the latest operator-wandb controller tag: %v\n", err)
				os.Exit(1)
			}

			// Get channel spec for new deployment
			_, err = deployer.GetChannelSpec("")
			if err != nil {
				fmt.Printf("Error getting channel spec: %v\n", err)
				os.Exit(1)
			}

			// 1. Scale down existing deployment
			fmt.Println("Scaling down existing W&B deployment...")
			scaleCmd := exec.Command("kubectl", "scale", "deployment", "wandb", "--replicas=0", "-n", namespace)
			if err := scaleCmd.Run(); err != nil {
				fmt.Printf("Error scaling down deployment: %v\n", err)
				os.Exit(1)
			}

			// 3. Install operator
			fmt.Println("Installing W&B operator...")
			operatorCmd := exec.Command("helm", "upgrade", "--install", "operator",
				"wandb/operator",
				"--version", latestTag,
				"-n", namespace,
				"--create-namespace")
			if err := operatorCmd.Run(); err != nil {
				fmt.Printf("Error installing operator: %v\n", err)
				os.Exit(1)
			}

			// 4. Create Wandb custom resource
			fmt.Println("Creating W&B custom resource...")
			// Note: This is a simplified example. In practice, you'd want to:
			// - Extract existing config from current deployment
			// - Convert it to the new CR format
			// - Apply the new CR
			crCmd := exec.Command("kubectl", "apply", "-f", "wandb-cr.yaml", "-n", namespace)
			if err := crCmd.Run(); err != nil {
				fmt.Printf("Error creating custom resource: %v\n", err)
				os.Exit(1)
			}

			fmt.Println("Migration completed successfully!")
			fmt.Println("Please verify your deployment is running correctly.")
			fmt.Println("Note: You may need to manually migrate your PersistentVolumeClaims and other resources.")
		},
	}

	cmd.Flags().StringVarP(&namespace, "namespace", "n", "wandb", "Namespace of the W&B deployment")

	return cmd
}
