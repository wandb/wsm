package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wandb/wsm/pkg/appadmin"
	"github.com/wandb/wsm/pkg/kubectl"
)

func init() {
	rootCmd.AddCommand(UserCmd())
}

func UserCmd() *cobra.Command {
	var kubeContext string

	cmd := &cobra.Command{
		Use:   "user",
		Short: "Administer W&B user accounts on an instance",
		Long: `Administer W&B user accounts on a running instance.

  admin                  grant or revoke instance-admin, through the app's own
                         updateUser mutation, authorized as YOU.
  migrate-email-domain   rewrite every account's email address from one domain
                         to another. Irreversible; dry-runs unless --confirm.

Both reach in-cluster Services. Run them from inside the cluster, or from
outside through a port-forward plus --app-address (which takes an https://
prefix for any hop that leaves the cluster network).`,
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

	cmd.AddCommand(userAdminCmd())
	cmd.AddCommand(userMigrateEmailDomainCmd())

	return cmd
}

// appAddrFlags are shared by both subcommands: the app's GraphQL host:port and
// the API key that authenticates to it.
func addAppFlags(cmd *cobra.Command) {
	cmd.Flags().String("app-address", "", "address of the W&B app's GraphQL endpoint: host:port for an in-cluster Service, or https://host for anything outside the cluster (default: $WANDB_APP_ADDRESS, else api:8080)")
	cmd.Flags().String("api-key", "", "W&B API key of an instance admin (default: $WANDB_API_KEY, which is preferred — a flag value lands in shell history and in process listings)")
}

// resolveAppCreds builds the credentials for the app from flags and environment.
//
// The CLI authenticates with an API key rather than a session cookie. That is
// both the only thing a terminal has and the arm that is not subject to
// gorilla's Origin/CSRF check, which refuses cookie-authenticated requests that
// carry no allowed Origin.
func resolveAppCreds(cmd *cobra.Command) (string, appadmin.Creds, error) {
	addr, _ := cmd.Flags().GetString("app-address")
	if addr == "" {
		addr = os.Getenv("WANDB_APP_ADDRESS")
	}
	if addr == "" {
		addr = "api:8080"
	}

	key, _ := cmd.Flags().GetString("api-key")
	if key == "" {
		key = os.Getenv("WANDB_API_KEY")
	}
	if key == "" {
		return "", appadmin.Creds{}, errors.New("no API key: pass --api-key or set WANDB_API_KEY to an instance admin's key")
	}

	// Normalize here so a malformed address is rejected before the credential is
	// anywhere near a request. A bare host:port stays http:// — that is what an
	// in-cluster Service is — and anything else must say https:// for itself.
	base, err := appadmin.BaseURL(addr)
	if err != nil {
		return "", appadmin.Creds{}, err
	}
	return base, appadmin.Creds{Authorization: "Bearer " + key}, nil
}

// adminState renders the instance-admin flag for a terminal.
func adminState(admin bool) string {
	if admin {
		return "an instance admin"
	}
	return "not an instance admin"
}

func userAdminCmd() *cobra.Command {
	var grant, revoke bool

	cmd := &cobra.Command{
		Use:   "admin <email-or-username>",
		Short: "Grant or revoke instance-admin for a user",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if grant == revoke {
				return errors.New("pass exactly one of --grant or --revoke")
			}
			addr, creds, err := resolveAppCreds(cmd)
			if err != nil {
				return err
			}

			user, changed, err := appadmin.SetAdmin(cmd.Context(), addr, creds, args[0], grant)
			if err != nil {
				return err
			}

			// Username only: the email came from the app and does not need to be
			// echoed to a terminal or a log to confirm the right account changed.
			if !changed {
				fmt.Printf("%s was already %s; nothing changed\n", user.Username, adminState(user.IsAdmin()))
				return nil
			}
			fmt.Printf("%s is now %s\n", user.Username, adminState(user.IsAdmin()))
			return nil
		},
	}

	cmd.Flags().BoolVar(&grant, "grant", false, "make the user an instance admin")
	cmd.Flags().BoolVar(&revoke, "revoke", false, "remove the user's instance-admin status")
	addAppFlags(cmd)

	return cmd
}

func userMigrateEmailDomainCmd() *cobra.Command {
	var oldDomain, newDomain string
	var confirm bool

	cmd := &cobra.Command{
		Use:   "migrate-email-domain",
		Short: "Rewrite every account's email address from one domain to another",
		Long: `Rewrite every account's email address from one domain to another.

Runs as a dry run unless --confirm is passed: it reports how many accounts and
how many rows match across every table that carries the address, and samples
their addresses, without writing. The real run is IRREVERSIBLE — no record of
the previous domain survives it — and is refused if the matching rows changed
between the preview and the confirmation.

Requires a W&B admin identity even though the write is made with the instance's
own MySQL credential, so an irreversible bulk rewrite always has an accountable
W&B account behind it.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if oldDomain == "" || newDomain == "" {
				return errors.New("both --from and --to are required")
			}
			namespace, _ := cmd.Flags().GetString("wandb-namespace")
			name, _ := cmd.Flags().GetString("wandb-name")

			// Validate before connecting so a typo costs nothing, and use the
			// NORMALIZED values from here on — the raw flags may carry whitespace
			// that would land in the LIKE pattern.
			oldDomain, err := appadmin.ValidateDomain(oldDomain)
			if err != nil {
				return err
			}
			newDomain, err := appadmin.ValidateDomain(newDomain)
			if err != nil {
				return err
			}

			// Prove a W&B admin identity first: the write below uses a service
			// credential and never consults the app, so nothing else would.
			addr, creds, err := resolveAppCreds(cmd)
			if err != nil {
				return err
			}
			who, err := appadmin.RequireAdmin(cmd.Context(), addr, creds)
			if err != nil {
				return err
			}

			db, err := appadmin.Connect(cmd.Context(), namespace, name)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			// Always plan first, so --confirm reports the same set it is about to
			// rewrite rather than whatever the user saw on an earlier run.
			plan, err := appadmin.PlanEmailDomainMigration(cmd.Context(), db, oldDomain, newDomain)
			if err != nil {
				return err
			}
			fmt.Printf("%d account(s) and %d row(s) match @%s\n", plan.Accounts, plan.Rows, oldDomain)
			for _, s := range plan.Samples {
				fmt.Printf("  %s\n", s)
			}
			if plan.Accounts > len(plan.Samples) {
				fmt.Printf("  … and %d more\n", plan.Accounts-len(plan.Samples))
			}
			if plan.Empty() {
				return nil
			}
			if !confirm {
				fmt.Printf("\nDry run. Re-run with --confirm to rewrite these to @%s.\n", newDomain)
				return nil
			}

			if !confirmOnStdin(fmt.Sprintf("Rewrite %d account(s) / %d row(s) from @%s to @%s? This cannot be undone",
				plan.Accounts, plan.Rows, oldDomain, newDomain)) {
				return errors.New("aborted")
			}

			// Passing the confirmed plan is what binds the write to the preview:
			// ApplyEmailDomainMigration re-scans under FOR UPDATE and refuses if
			// the fingerprint moved, so an account that started matching between
			// the preview and the yes cannot be swept in unreviewed.
			applied, err := appadmin.ApplyEmailDomainMigration(
				cmd.Context(), db, oldDomain, newDomain, plan)
			if err != nil {
				return err
			}
			fmt.Printf("Rewrote %d account(s) / %d row(s) to @%s (as %s)\n",
				applied.Accounts, applied.Rows, newDomain, who)
			return nil
		},
	}

	cmd.Flags().StringVar(&oldDomain, "from", "", "the domain to migrate away from, e.g. old-corp.com (required)")
	cmd.Flags().StringVar(&newDomain, "to", "", "the domain to migrate to, e.g. new-corp.com (required)")
	cmd.Flags().BoolVar(&confirm, "confirm", false, "perform the rewrite instead of a dry run")
	addAppFlags(cmd)

	return cmd
}

// confirmOnStdin asks for an explicit yes. Returns false when stdin is not a
// terminal, so a scripted invocation cannot be silently answered by an EOF.
func confirmOnStdin(prompt string) bool {
	fmt.Printf("%s [y/N]: ", prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}
