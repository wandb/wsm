package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/containers/image/v5/docker"
	"github.com/containers/image/v5/types"
	"github.com/spf13/cobra"
	"github.com/wandb/wsm/pkg/deployer"
	"github.com/wandb/wsm/pkg/helm"
	"github.com/wandb/wsm/pkg/utils"
	"gopkg.in/yaml.v3"
)

var registryKeepCache bool

func init() {
	rootCmd.AddCommand(RegistryCmd())
}

func RegistryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "registry",
		Short: "Check and describe how to mirror W&B images to a private registry",
		Long: `Tools for working with a mirrored container registry.

    wsm registry check   Verify each required W&B image is present in your registry.
    wsm registry values  Emit a values.yaml fragment that points the chart at
                         your registry instead of the public sources.

  Both subcommands re-use the same image discovery as 'wsm list' and 'wsm download',
  so they write a chart cache to ./bundle/charts as a side effect.`,
	}

	cmd.PersistentFlags().BoolVar(
		&registryKeepCache,
		"keep-cache",
		false,
		"Don't delete ./bundle/charts after running (speeds up repeated invocations)",
	)
	cmd.AddCommand(registryCheckCmd())
	cmd.AddCommand(registryValuesCmd())
	cmd.AddCommand(registryPushCmd())
	return cmd
}

func discoverImages() ([]string, error) {
	operatorTag, err := getMostRecentTag("wandb/controller")
	if err != nil {
		return nil, fmt.Errorf("get latest operator tag: %w", err)
	}

	operatorImgs, err := downloadChartImages(
		helm.WandbHelmRepoURL,
		helm.WandbOperatorChart,
		"",
		map[string]interface{}{
			"image": map[string]interface{}{"tag": operatorTag},
		},
	)
	if err != nil {
		return nil, fmt.Errorf("operator images: %w", err)
	}

	spec, err := deployer.GetChannelSpec("")
	if err != nil {
		return nil, fmt.Errorf("channel spec: %w", err)
	}
	if wt, ok := spec.Values["weave-trace"]; ok {
		wt.(map[string]interface{})["install"] = true
	}
	wandbImgs, err := downloadChartImages(spec.Chart.URL, spec.Chart.Name, spec.Chart.Version, spec.Values)
	if err != nil {
		return nil, fmt.Errorf("wandb images: %w", err)
	}

	return utils.RemoveDuplicates(append(operatorImgs, wandbImgs...)), nil
}

func translate(image, registry string) string {
	registry = strings.TrimRight(registry, "/")
	parts := strings.SplitN(image, "/", 2)
	if len(parts) == 2 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		return registry + "/" + parts[1]
	}
	return registry + "/" + image
}

// ---------------- wsm registry check ----------------

func registryCheckCmd() *cobra.Command {
	var (
		registry      string
		insecure      bool
		failOnMissing bool
	)

	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check that all required images exist in your mirrored registry",
		Long: `For each image required by W&B, do a manifest check against --registry
  and report whether the image is present, missing, or unauthorized.

  Auth is read from your Docker config (~/.docker/config.json) by default.
  Use --insecure for plain-HTTP or self-signed registries.`,
		Example: `  wsm registry check --registry myreg.example.com
    wsm registry check --registry myreg.example.com --insecure
    wsm registry check --registry myreg.example.com --fail-on-missing`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !registryKeepCache {
				defer cleanupChartCache()
			}
			if registry == "" {
				return fmt.Errorf("--registry is required")
			}

			fmt.Println("Discovering required images…")
			images, err := discoverImages()
			if err != nil {
				return err
			}

			fmt.Printf("Checking %d images against %s\n\n", len(images), registry)
			fmt.Printf("%-9s  %-65s  %s\n", "STATUS", "TARGET", "SOURCE")

			var present, missing, unauth, errs int
			ctx := context.Background()
			for _, src := range images {
				tgt := translate(src, registry)
				status, msg := checkOne(ctx, tgt, insecure)
				switch status {
				case "present":
					present++
				case "missing":
					missing++
				case "unauthorized":
					unauth++
				default:
					errs++
				}
				fmt.Printf("%-9s  %-65s  %s\n", status, tgt, src)
				if msg != "" {
					fmt.Printf("           └─ %s\n", msg)
				}
			}

			fmt.Printf("\n%d total — %d present, %d missing, %d auth issues, %d errors\n",
				len(images), present, missing, unauth, errs)

			if failOnMissing && (missing+errs) > 0 {
				return fmt.Errorf("%d image(s) not present in %s", missing+errs, registry)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&registry, "registry", "", "Target registry to check against, e.g. myreg.example.com (required)")
	cmd.Flags().BoolVar(&insecure, "insecure", false, "Skip TLS verification when contacting the registry")
	cmd.Flags().BoolVar(&failOnMissing, "fail-on-missing", false, "Exit non-zero if any image is missing")
	return cmd
}

func checkOne(ctx context.Context, image string, insecure bool) (status, errMsg string) {
	sysCtx := &types.SystemContext{}
	if insecure {
		sysCtx.DockerInsecureSkipTLSVerify = types.OptionalBoolTrue
	}

	ref, err := docker.ParseReference("//" + image)
	if err != nil {
		return "error", err.Error()
	}
	src, err := ref.NewImageSource(ctx, sysCtx)
	if err != nil {
		return classify(err)
	}
	defer func() { _ = src.Close() }()

	if _, _, err := src.GetManifest(ctx, nil); err != nil {
		return classify(err)
	}
	return "present", ""
}

func classify(err error) (status, errMsg string) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "manifest unknown"),
		strings.Contains(msg, "not found"),
		strings.Contains(msg, "404"):
		return "missing", ""
	case strings.Contains(msg, "401"),
		strings.Contains(msg, "403"),
		strings.Contains(msg, "unauthorized"):
		return "unauthorized", msg
	}
	return "error", msg
}

// ---------------- wsm registry values ----------------

func registryValuesCmd() *cobra.Command {
	var (
		registry string
		outFile  string
	)

	cmd := &cobra.Command{
		Use:   "values",
		Short: "Emit a values.yaml fragment that re-points images at your registry",
		Long: `Generate a values.yaml fragment that overrides each image reference to use
  your mirrored registry instead of the upstream source. Pipe to a file or use
  --output to write directly.`,
		Example: `  wsm registry values --registry myreg.example.com
    wsm registry values --registry myreg.example.com -o overrides.yaml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !registryKeepCache {
				defer cleanupChartCache()
			}
			if registry == "" {
				return fmt.Errorf("--registry is required")
			}

			fmt.Fprintln(os.Stderr, "Discovering required images…")
			images, err := discoverImages()
			if err != nil {
				return err
			}

			overrides := make([]map[string]string, 0, len(images))
			for _, src := range images {
				overrides = append(overrides, map[string]string{
					"source": src,
					"target": translate(src, registry),
				})
			}

			doc := map[string]interface{}{
				"global": map[string]interface{}{
					"imageRegistry": strings.TrimRight(registry, "/"),
				},
				"_imageOverrides": overrides,
			}
			y, err := yaml.Marshal(doc)
			if err != nil {
				return err
			}

			header := fmt.Sprintf("# Generated by wsm registry values\n# Registry: %s\n# Date: %s\n\n",
				registry, time.Now().UTC().Format(time.RFC3339))
			content := append([]byte(header), y...)

			if outFile == "" || outFile == "-" {
				_, err = os.Stdout.Write(content)
				return err
			}
			return os.WriteFile(outFile, content, 0644)
		},
	}

	cmd.Flags().StringVar(&registry, "registry", "", "Target registry, e.g. myreg.example.com (required)")
	cmd.Flags().StringVarP(&outFile, "output", "o", "-", "Output path (default: stdout)")
	return cmd
}

func cleanupChartCache() {
	_ = os.RemoveAll("bundle/charts")
	_ = os.Remove("bundle")
}
