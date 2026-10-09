package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/wandb/wsm/pkg/kubectl"
	supportbundle2 "github.com/wandb/wsm/pkg/observabaility/supportbundle"
	"github.com/wandb/wsm/pkg/operator"
	"golang.org/x/term"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func init() {
	rootCmd.AddCommand(SupportBundleCmd())
}

const supportBundlePollInterval = 3 * time.Second

func SupportBundleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "support-bundle",
		Aliases: []string{"supportbundle"},
		Short:   "Collect, list, retrieve and delete Lumen support bundles for a v2 W&B install",
		Long: `Collect diagnostics from a v2 W&B installation with Lumen.

wsm runs Lumen as a Kubernetes Job in the installation namespace. Lumen
redacts sensitive values and uploads the archive to the installation's own
bucket. The Job keeps running if wsm disconnects; use 'status' to reconnect.`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			kubeContext, _ := cmd.Flags().GetString("context")
			kubectl.SetContext(kubeContext)
			return nil
		},
	}
	cmd.PersistentFlags().String("context", "", "name of the kubeconfig context to use")
	cmd.PersistentFlags().String("wandb-name", "", "Name of the WeightsAndBiases resource (default: discovered when the cluster has one)")
	cmd.PersistentFlags().String("wandb-namespace", "", "Namespace of the WeightsAndBiases resource (default: discovered)")

	cmd.AddCommand(supportBundleCreateCmd(), supportBundleListCmd(), supportBundleStatusCmd(),
		supportBundleRetrieveCmd(), supportBundleCancelCmd(), supportBundleDeleteCmd())
	return cmd
}

// supportBundleTarget resolves the clients and installation selected by flags.
type supportBundleTarget struct {
	cfg     *rest.Config
	cs      *kubernetes.Clientset
	manager *supportbundle2.Manager
	ns      string
	name    string
}

func newSupportBundleTarget(cmd *cobra.Command) (*supportBundleTarget, error) {
	ns, _ := cmd.Flags().GetString("wandb-namespace")
	name, _ := cmd.Flags().GetString("wandb-name")
	cfg, cs, err := kubectl.GetClientset()
	if err != nil {
		return nil, err
	}
	if ns == "" || name == "" {
		ref, err := discoverInstallation(cmd.Context(), ns, name)
		if err != nil {
			return nil, err
		}
		ns, name = ref.Namespace, ref.Name
		fmt.Printf("Using WeightsAndBiases %s\n", ref)
	}
	return &supportBundleTarget{
		cfg: cfg, cs: cs, ns: ns, name: name,
		manager: &supportbundle2.Manager{Client: cs, Namespace: ns, Name: name},
	}, nil
}

// discoverInstallation lists v2 CRs (namespaced when --wandb-namespace is set)
// and selects the only match.
func discoverInstallation(ctx context.Context, namespace, name string) (supportbundle2.InstallationRef, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var refs []supportbundle2.InstallationRef
	if namespace != "" {
		names, err := operator.ListCRs(ctx, namespace)
		if err != nil {
			return supportbundle2.InstallationRef{}, discoveryError(err)
		}
		for _, n := range names {
			refs = append(refs, supportbundle2.InstallationRef{Namespace: namespace, Name: n})
		}
	} else {
		all, err := operator.ListAllCRs(ctx)
		if err != nil {
			return supportbundle2.InstallationRef{}, discoveryError(err)
		}
		for _, r := range all {
			refs = append(refs, supportbundle2.InstallationRef{Namespace: r.Namespace, Name: r.Name})
		}
	}
	return supportbundle2.SelectInstallation(refs, namespace, name)
}

func discoveryError(err error) error {
	if apierrors.IsForbidden(err) {
		return fmt.Errorf("cannot list WeightsAndBiases to discover the installation; pass --wandb-namespace and --wandb-name (see %s): %w", supportbundle2.PermissionsDoc, err)
	}
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("the v2 WeightsAndBiases CRD is not installed in this cluster: %w", err)
	}
	return err
}

func (t *supportBundleTarget) installation(ctx context.Context) (*supportbundle2.Installation, []string, error) {
	cr, err := operator.GetCR(ctx, t.name, t.ns)
	if err != nil {
		if apierrors.IsForbidden(err) {
			return nil, nil, fmt.Errorf("permission denied reading WeightsAndBiases %s/%s (see %s): %w", t.ns, t.name, supportbundle2.PermissionsDoc, err)
		}
		return nil, nil, err
	}
	return supportbundle2.FromCR(cr)
}

// objectDeleter opens the bucket lazily, only when a delete actually needs it.
func (t *supportBundleTarget) objectDeleter(inst *supportbundle2.Installation) (supportbundle2.ObjectDeleter, func()) {
	var (
		store  supportbundle2.ObjectStore
		bucket string
		closer = func() {}
	)
	return func(ctx context.Context, art *supportbundle2.Artifact) error {
		if store == nil {
			var err error
			store, bucket, closer, err = supportbundle2.OpenS3(ctx, t.cfg, t.cs, inst)
			if err != nil {
				return err
			}
		}
		key, err := supportbundle2.ObjectKey(art, bucket)
		if err != nil {
			return err
		}
		return store.Delete(ctx, key)
	}, func() { closer() }
}

func interruptContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func supportBundleCreateCmd() *cobra.Command {
	var (
		wait           bool
		since          time.Duration
		timeout        time.Duration
		retention      time.Duration
		skipTelemetry  bool
		lumenVersion   string
		imageRegistry  string
		pullSecrets    []string
		serviceAccount string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Start a support-bundle collection Job",
		Example: `  # Collect the last hour and watch until it finishes (Ctrl-C stops watching only)
  wsm support-bundle create --wait

  # Wider window, no telemetry, explicit installation
  wsm support-bundle create --since 6h --skip-telemetry --wandb-namespace wandb --wandb-name wandb`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := interruptContext()
			defer cancel()

			if since <= 0 || timeout <= 0 || retention <= 0 {
				return fmt.Errorf("--since, --timeout and --retention must be positive")
			}
			if err := operator.ValidateImagePullSecretNames(pullSecrets); err != nil {
				return err
			}

			target, err := newSupportBundleTarget(cmd)
			if err != nil {
				return err
			}
			inst, warnings, err := target.installation(ctx)
			if err != nil {
				return err
			}
			for _, w := range warnings {
				fmt.Printf("⚠ %s\n", w)
			}

			image, err := supportbundle2.ResolveImage(inst, imageRegistry, lumenVersion)
			if err != nil {
				return err
			}
			setup := supportbundle2.SetupOptions{
				ServiceAccount:       supportbundle2.DefaultServiceAccountName,
				CreateServiceAccount: true,
				IncludeTelemetry:     !skipTelemetry && inst.Telemetry != nil,
			}
			if serviceAccount != "" {
				if serviceAccount == inst.AppServiceAccount {
					return fmt.Errorf("--service-account %q is the W&B application ServiceAccount; use a dedicated account", serviceAccount)
				}
				setup.ServiceAccount, setup.CreateServiceAccount = serviceAccount, false
			}

			if err := supportbundle2.Preflight(ctx, target.cs, supportbundle2.CreateAccess(inst, setup)); err != nil {
				return err
			}

			records, err := target.manager.List(ctx)
			if err != nil {
				return err
			}
			if expired := target.manager.Expired(records); len(expired) > 0 {
				deleter, closeStore := target.objectDeleter(inst)
				for _, rec := range expired {
					if err := target.manager.Delete(ctx, rec, deleter); err != nil {
						fmt.Printf("⚠ could not prune expired bundle %s: %v\n", rec.ID, err)
						continue
					}
					fmt.Printf("Pruned expired bundle %s (created %s)\n", rec.ID, rec.Created.Format("2006-01-02 15:04"))
				}
				closeStore()
			}
			if active := supportbundle2.ActiveRuns(records); len(active) > 0 {
				fmt.Printf("⚠ %d collection(s) already in progress for this installation (%s)\n", len(active), active[0].ID)
			}

			start := time.Now()
			fmt.Print("Preparing collector identity and permissions...")
			if err := supportbundle2.EnsureSetup(ctx, target.cs, inst, setup); err != nil {
				fmt.Println(" ✗")
				return err
			}
			fmt.Printf(" ✓ (%s)\n", time.Since(start).Round(time.Second))

			specYAML, sources, err := supportbundle2.BuildCollectionSpec(inst, since, setup.IncludeTelemetry)
			if err != nil {
				return err
			}
			pullRefs := append([]corev1.LocalObjectReference{}, inst.ImagePullSecrets...)
			for _, name := range pullSecrets {
				pullRefs = append(pullRefs, corev1.LocalObjectReference{Name: name})
			}

			var rec *supportbundle2.Record
			for attempt := 0; ; attempt++ {
				id, err := supportbundle2.NewBundleID()
				if err != nil {
					return err
				}
				rec, err = target.manager.Create(ctx, inst, supportbundle2.RunOptions{
					ID: id, Image: image, ServiceAccount: setup.ServiceAccount, PullSecrets: pullRefs,
					Created: time.Now(), Window: since, Timeout: timeout, Retention: retention, Sources: sources,
				}, specYAML)
				if err != nil && rec == nil && apierrors.IsAlreadyExists(err) && attempt < 3 {
					continue
				}
				if err != nil {
					if rec != nil {
						fmt.Printf("✗ bundle %s was partially created; remove it with: wsm support-bundle delete %s\n", rec.ID, rec.ID)
					}
					return err
				}
				break
			}

			fmt.Printf("\nBundle ID: %s\nJob:       %s/%s\nImage:     %s\nWindow:    %s\nSources:   %s\n",
				rec.ID, target.ns, rec.JobName, image, formatWindow(rec), strings.Join(rec.Sources, ", "))
			if !wait {
				fmt.Printf("\nCheck progress with: wsm support-bundle status %s --watch\n", rec.ID)
				return nil
			}
			fmt.Println()
			return watchBundle(ctx, target, rec.ID)
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "Watch the collection until it finishes (Ctrl-C stops watching; the Job keeps running)")
	cmd.Flags().DurationVar(&since, "since", time.Hour, "How far back to collect logs and telemetry")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "Deadline for the collection Job")
	cmd.Flags().DurationVar(&retention, "retention", 24*time.Hour, "How long to keep the bundle before it is pruned")
	cmd.Flags().BoolVar(&skipTelemetry, "skip-telemetry", false, "Skip Victoria metrics/logs/traces collection")
	cmd.Flags().StringVar(&lumenVersion, "lumen-version", supportbundle2.DefaultLumenVersion, "Lumen image tag")
	cmd.Flags().StringVar(&imageRegistry, "image-registry", "", "Registry to pull Lumen from (default: spec.global.imageRegistry, else the W&B public registry)")
	cmd.Flags().StringArrayVar(&pullSecrets, "image-pull-secret", nil, "Additional image pull Secret for the Lumen image (repeatable)")
	cmd.Flags().StringVar(&serviceAccount, "service-account", "", "Existing, pre-authorized ServiceAccount to run Lumen as (default: wsm-managed "+supportbundle2.DefaultServiceAccountName+")")
	return cmd
}

func watchBundle(ctx context.Context, target *supportBundleTarget, id string) error {
	rec, err := target.manager.Watch(ctx, id, os.Stdout, supportBundlePollInterval)
	if errors.Is(err, context.Canceled) {
		fmt.Printf("\nStopped watching; the collection keeps running in the cluster.\nReconnect with: wsm support-bundle status %s --watch\nCancel with:    wsm support-bundle cancel %s\n", id, id)
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Println()
	printBundle(rec, time.Now())
	if rec.Phase == supportbundle2.PhaseFailed {
		return fmt.Errorf("support bundle %s failed", id)
	}
	return nil
}

func supportBundleListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List support bundles for the installation, newest first",
		Example: `  wsm support-bundle list
  wsm support-bundle list --context my-cluster`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := newSupportBundleTarget(cmd)
			if err != nil {
				return err
			}
			records, err := target.manager.List(cmd.Context())
			if err != nil {
				return err
			}
			if len(records) == 0 {
				fmt.Printf("No support bundles for %s/%s\n", target.ns, target.name)
				return nil
			}
			now := time.Now()
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "BUNDLE ID\tCREATED (UTC)\tWINDOW\tSTATUS\tSIZE\tEXPIRES")
			for _, rec := range records {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", rec.ID, rec.Created.Format("2006-01-02 15:04"),
					formatWindow(rec), rec.DisplayPhase(now), formatSize(rec.Artifact()), formatExpiry(rec))
			}
			return w.Flush()
		},
	}
}

func supportBundleStatusCmd() *cobra.Command {
	var watch bool
	cmd := &cobra.Command{
		Use:   "status <bundle-id>",
		Short: "Show collection progress and the result of a support bundle",
		Example: `  wsm support-bundle status sb-7f92ac

  # Reconnect to a running collection
  wsm support-bundle status sb-7f92ac --watch`,
		Args: requireBundleID,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := interruptContext()
			defer cancel()
			target, err := newSupportBundleTarget(cmd)
			if err != nil {
				return err
			}
			rec, obs, err := target.manager.Get(ctx, args[0])
			if err != nil {
				return err
			}
			if watch && !rec.Terminal() {
				return watchBundle(ctx, target, rec.ID)
			}
			printBundle(rec, time.Now())
			for _, p := range obs.Problems {
				fmt.Printf("  ⚠ %s\n", p)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&watch, "watch", false, "Watch until the collection finishes (Ctrl-C stops watching)")
	return cmd
}

func supportBundleRetrieveCmd() *cobra.Command {
	var (
		output              string
		deleteAfterDownload bool
	)
	cmd := &cobra.Command{
		Use:   "retrieve <bundle-id>",
		Short: "Download a support bundle to this workstation",
		Example: `  wsm support-bundle retrieve sb-7f92ac -o ./sb-7f92ac.tgz

  # Delete the bucket copy once size and SHA-256 are verified
  wsm support-bundle retrieve sb-7f92ac -o ./sb-7f92ac.tgz --delete-after-download`,
		Args: requireBundleID,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := interruptContext()
			defer cancel()
			target, err := newSupportBundleTarget(cmd)
			if err != nil {
				return err
			}
			rec, _, err := target.manager.Get(ctx, args[0])
			if err != nil {
				return err
			}
			art := rec.Artifact()
			switch {
			case rec.Phase == supportbundle2.PhaseDeleted:
				return fmt.Errorf("bundle %s was already deleted from the bucket", rec.ID)
			case art == nil && rec.Phase == supportbundle2.PhaseCompleted:
				return fmt.Errorf("bundle %s: %s", rec.ID, supportbundle2.ResultUnavailable)
			case art == nil:
				return fmt.Errorf("bundle %s is %s; nothing to retrieve", rec.ID, rec.Phase)
			}
			if output == "" {
				output = rec.ID + ".tar.gz"
			}

			inst, _, err := target.installation(ctx)
			if err != nil {
				return err
			}
			store, bucket, closeStore, err := supportbundle2.OpenS3(ctx, target.cfg, target.cs, inst)
			if err != nil {
				return err
			}
			defer closeStore()
			key, err := supportbundle2.ObjectKey(art, bucket)
			if err != nil {
				return err
			}

			fmt.Printf("Downloading %s to %s...\n", art.Location, output)
			v, err := supportbundle2.Download(ctx, store, key, output, art)
			if err != nil {
				return err
			}
			fmt.Printf("✓ %s (%s, sha256 %s)\n", output, humanBytes(v.Size), v.SHA256)
			if !v.SizeVerified || !v.SHA256Verified {
				fmt.Println("⚠ Lumen did not report size and checksum for this bundle, so the download could not be verified against them")
			}
			if !deleteAfterDownload {
				return nil
			}
			if !v.Verified() {
				return fmt.Errorf("not deleting the bucket copy: size and sha256 could not be verified against the collector's result")
			}
			if err := store.Delete(ctx, key); err != nil {
				return err
			}
			if err := target.manager.MarkDeleted(ctx, rec); err != nil {
				return err
			}
			fmt.Println("✓ Deleted the bucket copy after verifying the download")
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "Destination file (default: <bundle-id>.tar.gz)")
	cmd.Flags().BoolVar(&deleteAfterDownload, "delete-after-download", false, "Delete the bucket copy once size and sha256 are verified")
	return cmd
}

func supportBundleCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "cancel <bundle-id>",
		Short:   "Stop a running collection (the bundle record is kept)",
		Example: `  wsm support-bundle cancel sb-7f92ac`,
		Args:    requireBundleID,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := interruptContext()
			defer cancel()
			target, err := newSupportBundleTarget(cmd)
			if err != nil {
				return err
			}
			rec, _, err := target.manager.Get(ctx, args[0])
			if err != nil {
				return err
			}
			if err := target.manager.Cancel(ctx, rec); err != nil {
				return err
			}
			fmt.Printf("✓ Cancelled %s (Job %s stopped); remove it with: wsm support-bundle delete %s\n", rec.ID, rec.JobName, rec.ID)
			return nil
		},
	}
}

func supportBundleDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <bundle-id>",
		Short: "Delete a bundle and its bucket object (prompts to cancel a running collection)",
		Example: `  wsm support-bundle delete sb-7f92ac

  # Cancel and delete a running collection without prompting
  wsm support-bundle delete sb-7f92ac --yes`,
		Args: requireBundleID,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := interruptContext()
			defer cancel()
			target, err := newSupportBundleTarget(cmd)
			if err != nil {
				return err
			}
			rec, _, err := target.manager.Get(ctx, args[0])
			if err != nil {
				return err
			}
			if !rec.Terminal() {
				question := fmt.Sprintf("Bundle %s is still collecting (%s). Cancel it and delete?", rec.ID, rec.Phase)
				ok, err := confirm(question, yes)
				if err != nil {
					return err
				}
				if !ok {
					return fmt.Errorf("not deleted; %s is still collecting", rec.ID)
				}
				if err := target.manager.Cancel(ctx, rec); err != nil {
					return err
				}
				fmt.Printf("✓ Cancelled %s\n", rec.ID)
			}
			var deleter supportbundle2.ObjectDeleter
			if rec.Artifact() != nil && rec.Phase != supportbundle2.PhaseDeleted {
				inst, _, err := target.installation(ctx)
				if err != nil {
					return err
				}
				var closeStore func()
				deleter, closeStore = target.objectDeleter(inst)
				defer closeStore()
			}
			if err := target.manager.Delete(ctx, rec, deleter); err != nil {
				return err
			}
			fmt.Printf("✓ Deleted bundle %s\n", rec.ID)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Cancel a running collection without prompting")
	return cmd
}

// confirm asks a yes/no question on the terminal. Without a terminal it
// refuses unless --yes was given, so scripts never cancel by accident.
func confirm(question string, assumeYes bool) (bool, error) {
	if assumeYes {
		return true, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, fmt.Errorf("%s Re-run with --yes, or use 'wsm support-bundle cancel' first", question)
	}
	fmt.Printf("%s [y/N]: ", question)
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

// requireBundleID replaces cobra's bare arity error with usage, examples and
// a pointer to 'list', since root silences usage on errors.
func requireBundleID(cmd *cobra.Command, args []string) error {
	if len(args) == 1 {
		return nil
	}
	problem := "missing <bundle-id>"
	if len(args) > 1 {
		problem = fmt.Sprintf("expected one <bundle-id>, got %d arguments", len(args))
	}
	return fmt.Errorf("%s\n\nUsage:\n  %s\n\nExamples:\n%s\n\nFind bundle IDs with: wsm support-bundle list",
		problem, cmd.UseLine(), cmd.Example)
}

func printBundle(rec *supportbundle2.Record, now time.Time) {
	fmt.Printf("Bundle:    %s (%s)\n", rec.ID, rec.Installation)
	fmt.Printf("Status:    %s\n", rec.DisplayPhase(now))
	if rec.Message != "" {
		fmt.Printf("Detail:    %s\n", rec.Message)
	}
	fmt.Printf("Created:   %s UTC\n", rec.Created.Format("2006-01-02 15:04:05"))
	fmt.Printf("Window:    %s\n", formatWindow(rec))
	fmt.Printf("Job:       %s\n", rec.JobName)
	fmt.Printf("Sources:   %s (per-source results are not reported by Lumen yet)\n", strings.Join(rec.Sources, ", "))
	if art := rec.Artifact(); art != nil {
		fmt.Printf("Location:  %s\n", art.Location)
		fmt.Printf("Size:      %s\n", formatSize(art))
		checksum := art.SHA256
		if checksum == "" {
			checksum = "not reported"
		}
		fmt.Printf("SHA-256:   %s\n", checksum)
	}
	fmt.Printf("Expires:   %s\n", formatExpiry(rec))
	if len(rec.Health) > 0 {
		fmt.Println("Health at collection time:")
		for _, h := range rec.Health {
			fmt.Printf("  - %s\n", h)
		}
	}
}

func formatWindow(rec *supportbundle2.Record) string {
	return fmt.Sprintf("%s–%s UTC", rec.WindowStart.Format("15:04"), rec.WindowEnd.Format("15:04"))
}

func formatExpiry(rec *supportbundle2.Record) string {
	if rec.Phase == supportbundle2.PhaseDeleted || rec.Expires.IsZero() {
		return "—"
	}
	return rec.Expires.Format("2006-01-02 15:04")
}

func formatSize(art *supportbundle2.Artifact) string {
	if art == nil || art.SizeBytes == 0 {
		return "—"
	}
	return humanBytes(art.SizeBytes)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.0f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
