package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wandb/wsm/pkg/kubectl"
	"github.com/wandb/wsm/pkg/license"
	"github.com/wandb/wsm/pkg/operator"
)

func init() {
	rootCmd.AddCommand(LicenseCmd())
}

func LicenseCmd() *cobra.Command {
	var kubeContext string

	cmd := &cobra.Command{
		Use:   "license",
		Short: "Inspect and update the W&B license on an instance",
		Long:  `Read or set spec.wandb.license on a WeightsAndBiases CR`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if kubeContext == "" {
				return errors.New("--context is required")
			}
			kubectl.SetContext(kubeContext)
			return nil
		},
	}

	cmd.PersistentFlags().StringVar(&kubeContext, "context", "", "name of the kubeconfig context to use (required)")
	cmd.PersistentFlags().String("wandb-name", "wandb", "Name of the W&B instance")
	cmd.PersistentFlags().String("wandb-namespace", "wandb", "Namespace of the W&B instance")

	cmd.AddCommand(licenseInfoCmd())
	cmd.AddCommand(licenseSetCmd())

	return cmd
}

func licenseInfoCmd() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "info",
		Short: "Show information from license for a W&B instance",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			wandbName, _ := cmd.Flags().GetString("wandb-name")
			wandbNamespace, _ := cmd.Flags().GetString("wandb-namespace")

			cr, err := operator.GetCR(ctx, wandbName, wandbNamespace)
			if err != nil {
				return err
			}

			claims, err := license.Decode(cr.Spec.Wandb.License)
			if errors.Is(err, license.ErrNoLicense) {
				fmt.Println("No License Set!")
				return nil
			}
			if err != nil {
				return err
			}

			if asJSON {
				out, err := json.MarshalIndent(claims, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(out) + "\n")
				return nil
			}

			fmt.Print(formatLicenseInfo(claims) + "\n")
			return nil
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the decoded claims as JSON")

	return cmd
}

// Add or remove an infoRow to change the `license info` output.
type infoRow struct {
	label string
	value func(c *license.Claims) (string, bool)
}

var infoRows = []infoRow{
	{"Expiry", func(c *license.Claims) (string, bool) {
		t, ok := c.ExpiresAt()
		if !ok {
			return "", false
		}
		s := t.UTC().Format("2006-01-02 at 3:04 PM MST")
		if c.Expired() {
			s += " (expired)"
		}
		return s, true
	}},
	{"Trial", func(c *license.Claims) (string, bool) {
		if c.Trial == nil {
			return "", false
		}
		return strconv.FormatBool(*c.Trial), true
	}},
	{"Max teams", func(c *license.Claims) (string, bool) { return intRow(c.MaxTeamsResolved()) }},
	{"Max users", func(c *license.Claims) (string, bool) { return intRow(c.Users()) }},
	{"Max view-only users", func(c *license.Claims) (string, bool) { return intRow(c.ViewOnlyUsers()) }},
	{"Flags", func(c *license.Claims) (string, bool) {
		if len(c.Flags) == 0 {
			return "", false
		}
		return strings.Join(c.Flags, ", "), true
	}},
	{"Deployment ID", func(c *license.Claims) (string, bool) { return c.DeploymentID, c.DeploymentID != "" }},
	{"Deploy link", func(c *license.Claims) (string, bool) { l := c.Link(); return l, l != "" }},
}

func formatLicenseInfo(c *license.Claims) string {
	var b strings.Builder
	for _, row := range infoRows {
		if v, ok := row.value(c); ok {
			fmt.Fprintf(&b, "%-20s %s\n", row.label+":", v)
		}
	}
	return b.String()
}

func intRow(v int, ok bool) (string, bool) {
	if !ok {
		return "", false
	}
	return strconv.Itoa(v), true
}

func licenseSetCmd() *cobra.Command {
	var (
		licenseValue string
		licenseFile  string
		clear        bool
		force        bool
		dryRun       bool
	)

	cmd := &cobra.Command{
		Use:   "set",
		Short: "Set or clear the license on a W&B instance",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			wandbName, _ := cmd.Flags().GetString("wandb-name")
			wandbNamespace, _ := cmd.Flags().GetString("wandb-namespace")

			newLicense, err := resolveLicenseInput(licenseValue, licenseFile, clear)
			if err != nil {
				return err
			}

			hasMarker, err := kubectl.HasDeploymentMarker(ctx, wandbNamespace, "wandb-cr")
			if err != nil {
				return err
			}
			if !hasMarker && !force {
				return fmt.Errorf("no wsm deployment marker found in namespace %q — refusing to modify an install wsm did not deploy (pass --force to override)", wandbNamespace)
			}

			if clear {
				fmt.Printf("Clearing license on %s/%s\n", wandbNamespace, wandbName)
			} else {
				if err := license.Validate(newLicense); err != nil {
					return fmt.Errorf("invalid license: %w", err)
				}
				claims, err := license.Decode(newLicense)
				if err != nil {
					return err
				}
				fmt.Printf("Setting license on %s/%s:\n", wandbNamespace, wandbName)
				fmt.Print(formatLicenseInfo(claims))
			}

			if dryRun {
				fmt.Println("(dry-run) no changes applied.")
				return nil
			}

			fmt.Print("Proceed? [y/N]: ")
			answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if strings.ToLower(strings.TrimSpace(answer)) != "y" {
				fmt.Println("aborted.")
				return nil
			}

			if err := operator.UpdateLicense(ctx, wandbName, wandbNamespace, newLicense); err != nil {
				return fmt.Errorf("failed to update license: %w", err)
			}
			fmt.Println("License updated.")
			return nil
		},
	}

	cmd.Flags().StringVar(&licenseValue, "license", "", "W&B license string")
	cmd.Flags().StringVar(&licenseFile, "license-file", "", "Path to a file containing the W&B license")
	cmd.Flags().BoolVar(&clear, "clear", false, "Clear the license (set to empty)")
	cmd.Flags().BoolVar(&force, "force", false, "Modify an install even without a wsm deployment marker")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would change without applying")

	return cmd
}

func resolveLicenseInput(licenseValue, licenseFile string, clear bool) (string, error) {
	set := 0
	for _, on := range []bool{licenseValue != "", licenseFile != "", clear} {
		if on {
			set++
		}
	}
	if set == 0 {
		return "", errors.New("one of --license, --license-file, or --clear is required")
	}
	if set > 1 {
		return "", errors.New("--license, --license-file, and --clear are mutually exclusive")
	}

	if clear {
		return "", nil
	}
	if licenseValue != "" {
		return strings.TrimSpace(licenseValue), nil
	}
	data, err := os.ReadFile(licenseFile)
	if err != nil {
		return "", fmt.Errorf("failed to read license file: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}
