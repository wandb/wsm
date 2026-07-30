package main

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/spf13/cobra"
)

var ecrHostRe = regexp.MustCompile(`^[0-9]+\.dkr\.ecr\.([a-z0-9-]+)\.amazonaws\.com$`)

func registryCreateReposCmd() *cobra.Command {
	var (
		targetRegistry       string
		operatorChartVersion string
		wandbVersion         string
		skipManaged          bool
		region               string
		dryRun               bool
	)

	cmd := &cobra.Command{
		Use:   "create-repos",
		Short: "Pre-create the ECR repositories 'wsm registry mirror' pushes to (Amazon ECR only)",
		Long: `Amazon ECR does not create a repository on first push (a push to a missing
repo fails with "name unknown"). Every other registry wsm targets creates the
repository implicitly, so this command is needed only for ECR.

create-repos computes the exact destination set 'wsm registry mirror' would push
to — using the SAME --to / --operator-chart-version / --wandb-version /
--skip-managed-images flags — and creates each repository in ECR. Run it once,
before mirroring. It is idempotent: repositories that already exist are skipped.

Credentials come from your AWS config (env / ~/.aws / IRSA) via the 'aws' CLI —
the same credentials you use for 'aws ecr get-login-password'. The caller needs
IAM permission ecr:CreateRepository.`,
		Example: `  # Create every repo the mirror needs under an ECR path prefix.
  wsm registry create-repos \
    --to 770934259321.dkr.ecr.us-west-2.amazonaws.com/wandbench/collinol-test \
    --operator-chart-version 2.0.0-beta.1 --wandb-version 0.83.0-pre.1

  # Preview the repositories without creating them.
  wsm registry create-repos --to <acct>.dkr.ecr.us-west-2.amazonaws.com/wandb --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if targetRegistry == "" {
				return fmt.Errorf("--to is required (your ECR registry, e.g. <acct>.dkr.ecr.<region>.amazonaws.com[/<prefix>])")
			}
			targetRegistry = strings.TrimRight(targetRegistry, "/")

			host := strings.SplitN(targetRegistry, "/", 2)[0]
			m := ecrHostRe.FindStringSubmatch(host)
			if m == nil {
				return fmt.Errorf("--to host %q is not an Amazon ECR registry (expected <account>.dkr.ecr.<region>.amazonaws.com[/<prefix>]); other registries create repositories automatically on push, so this command is unnecessary for them", host)
			}
			if region == "" {
				region = m[1]
			}

			repos, err := collectMirrorRepos(cmd.Context(), targetRegistry, operatorChartVersion, wandbVersion, skipManaged)
			if err != nil {
				return err
			}

			fmt.Printf("%d repositories for %s (region %s)\n\n", len(repos), host, region)

			if dryRun {
				for _, r := range repos {
					fmt.Printf("  %s\n", r)
				}
				return nil
			}

			var created, existed, failed int
			for _, repo := range repos {
				out, err := exec.CommandContext(cmd.Context(), "aws", "ecr", "create-repository",
					"--repository-name", repo, "--region", region).CombinedOutput()
				switch {
				case err == nil:
					fmt.Printf("  ✓ created  %s\n", repo)
					created++
				case strings.Contains(string(out), "RepositoryAlreadyExistsException"):
					fmt.Printf("  • exists   %s\n", repo)
					existed++
				default:
					fmt.Printf("  ✗ failed   %s: %s\n", repo, strings.TrimSpace(string(out)))
					failed++
				}
			}

			fmt.Printf("\n%d created, %d already existed, %d failed\n", created, existed, failed)
			if failed > 0 {
				return fmt.Errorf("%d repository(ies) failed to create", failed)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&targetRegistry, "to", "", "ECR registry (and optional path prefix), e.g. <acct>.dkr.ecr.us-west-2.amazonaws.com/wandbench/collinol-test (required)")
	cmd.Flags().StringVar(&operatorChartVersion, "operator-chart-version", "2.0.0-beta.1", "Operator chart version; must match the value passed to 'wsm registry mirror'")
	cmd.Flags().StringVar(&wandbVersion, "wandb-version", "", "W&B server version; when set, also create the server-manifest repo and every application-image repo the manifest references")
	cmd.Flags().BoolVar(&skipManaged, "skip-managed-images", false, "Skip the managed-service operator + data-plane repos; match the flag you mirror with")
	cmd.Flags().StringVar(&region, "region", "", "AWS region for create-repository (default: parsed from the ECR host)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the repositories that would be created, without creating them")
	return cmd
}

func collectMirrorRepos(ctx context.Context, target, operatorChartVersion, wandbVersion string, skipManaged bool) ([]string, error) {
	seen := map[string]bool{}
	var repos []string
	add := func(ref string) {
		repo := repoNameFromRef(ref)
		if repo == "" || seen[repo] {
			return
		}
		seen[repo] = true
		repos = append(repos, repo)
	}

	for _, it := range buildMirrorPlan(target, operatorChartVersion) {
		add(it.dst)
	}
	if !skipManaged {
		for _, it := range buildManagedImagePlan(target) {
			add(it.dst)
		}
	}

	if wandbVersion != "" {
		files, err := pullManifestYAML(ctx, wandbVersion)
		if err != nil {
			return nil, fmt.Errorf("pull server manifest %s to enumerate application images: %w", wandbVersion, err)
		}
		refs, err := collectManifestImages(files)
		if err != nil {
			return nil, fmt.Errorf("enumerate manifest images: %w", err)
		}
		for _, ref := range refs {
			add(mirrorImageRef(target, ref))
		}
		add(target + "/wandb/server-manifest:" + wandbVersion)
	}

	sort.Strings(repos)
	return repos, nil
}

func repoNameFromRef(ref string) string {
	r, err := name.ParseReference(ref)
	if err != nil {
		return ""
	}
	return r.Context().RepositoryStr()
}
