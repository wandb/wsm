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
		Long: `Promote or demote instance admins, and migrate accounts between email domains.

These act on the W&B application's own data rather than on the CR, and the two
take different routes:

  admin                  the app's updateUser mutation, authorized as YOU. The app
                         enforces its own rules, so you must be an instance admin
                         and cannot revoke your own flag.
  migrate-email-domain   SQL against the instance's MySQL, because the app offers
                         no way to change another user's address. Irreversible,
                         and requires --confirm after a dry run.

Both need to reach in-cluster Services, so run them from inside the cluster or
through a port-forward.`,
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
	cmd.Flags().String("app-address", "", "host:port serving the W&B app's GraphQL endpoint (default: $WANDB_APP_ADDRESS, else api:8080)")
	cmd.Flags().String("api-key", "", "W&B API key of an instance admin (default: $WANDB_API_KEY)")
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
	return addr, appadmin.Creds{Authorization: "Bearer " + key}, nil
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

			user, err := appadmin.SetAdmin(cmd.Context(), addr, creds, args[0], grant)
			if err != nil {
				return err
			}

			state := "no longer an admin"
			if user.IsAdmin() {
				state = "now an admin"
			}
			fmt.Printf("%s (%s) is %s\n", user.Username, user.Email, state)
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

Runs as a dry run unless --confirm is passed: it reports how many accounts match
and samples their addresses, then rolls back. The real run is IRREVERSIBLE — no
record of the previous domain survives it.

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

			// Always dry-run first, so --confirm reports the same set it is about
			// to rewrite rather than whatever the user saw earlier.
			matched, _, samples, _, err := appadmin.MigrateEmailDomain(
				cmd.Context(), db, oldDomain, newDomain, true, nil)
			if err != nil {
				return err
			}
			fmt.Printf("%d account(s) match @%s\n", matched, oldDomain)
			for _, s := range samples {
				fmt.Printf("  %s\n", s)
			}
			if matched > len(samples) {
				fmt.Printf("  … and %d more\n", matched-len(samples))
			}
			if matched == 0 {
				return nil
			}
			if !confirm {
				fmt.Printf("\nDry run. Re-run with --confirm to rewrite these to @%s.\n", newDomain)
				return nil
			}

			if !confirmOnStdin(fmt.Sprintf("Rewrite %d account(s) from @%s to @%s? This cannot be undone",
				matched, oldDomain, newDomain)) {
				return errors.New("aborted")
			}

			_, updated, _, _, err := appadmin.MigrateEmailDomain(
				cmd.Context(), db, oldDomain, newDomain, false, nil)
			if err != nil {
				return err
			}
			fmt.Printf("Rewrote %d account(s) to @%s (as %s)\n", updated, newDomain, who)
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
