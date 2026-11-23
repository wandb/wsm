package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Flag help groups. A flag joins a group through the flagGroupAnnotation, and
// groupedFlagUsages prints one block per group in flagGroupOrder. Flags without a
// group keep Cobra's default "Flags:" / "Global Flags:" blocks, so commands that
// never call setFlagGroup render exactly as before.
const flagGroupAnnotation = "wsm_flag_group"

const (
	flagGroupTarget        = "Target cluster and instance"
	flagGroupOperator      = "Operator"
	flagGroupSharedInfra   = "Shared infrastructure (cert-manager, nginx-gateway, kube-state-metrics)"
	flagGroupKind          = "Local Kind cluster"
	flagGroupInstance      = "W&B instance"
	flagGroupNetworking    = "Networking and TLS"
	flagGroupAirgap        = "Air-gapped / private registry"
	flagGroupStorage       = "Storage"
	flagGroupOIDC          = "Authentication (OIDC)"
	flagGroupSecurity      = "Security and retention"
	flagGroupNotifications = "Notifications (email, SMTP, Slack)"
	flagGroupProxy         = "Egress proxy"
	flagGroupObservability = "Observability"
	flagGroupWatchtower    = "Watchtower"
	flagGroupAdvanced      = "Advanced: raw CR overrides"
)

var flagGroupOrder = []string{
	flagGroupTarget,
	flagGroupOperator,
	flagGroupSharedInfra,
	flagGroupKind,
	flagGroupInstance,
	flagGroupNetworking,
	flagGroupAirgap,
	flagGroupStorage,
	flagGroupOIDC,
	flagGroupSecurity,
	flagGroupNotifications,
	flagGroupProxy,
	flagGroupObservability,
	flagGroupWatchtower,
	flagGroupAdvanced,
}

// The Flags/Global Flags section of Cobra's default usage template (v1.10.2),
// replaced in init with a call to groupedFlagUsages.
const defaultFlagsTemplate = `{{if .HasAvailableLocalFlags}}

Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

Global Flags:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}`

func init() {
	cobra.AddTemplateFunc("groupedFlagUsages", groupedFlagUsages)
	// If a Cobra upgrade changes the default template, this is a no-op and help
	// falls back to Cobra's ungrouped output; TestUsageTemplateGroupsFlags catches it.
	rootCmd.SetUsageTemplate(strings.Replace(rootCmd.UsageTemplate(), defaultFlagsTemplate, "{{groupedFlagUsages .}}", 1))
}

// setFlagGroup assigns the named flags in fs to a help group.
func setFlagGroup(fs *pflag.FlagSet, group string, names ...string) {
	for _, n := range names {
		if err := fs.SetAnnotation(n, flagGroupAnnotation, []string{group}); err != nil {
			panic(fmt.Sprintf("setFlagGroup: %v", err))
		}
	}
}

// groupedFlagUsages renders cmd's local and inherited flags: grouped flags first,
// in flagGroupOrder, then the ungrouped ones under "Flags:" and "Global Flags:".
func groupedFlagUsages(cmd *cobra.Command) string {
	sets := map[string]*pflag.FlagSet{}
	add := func(fallback string) func(*pflag.Flag) {
		return func(f *pflag.Flag) {
			if f.Hidden {
				return
			}
			group := fallback
			if v := f.Annotations[flagGroupAnnotation]; len(v) > 0 {
				group = v[0]
			}
			if sets[group] == nil {
				sets[group] = pflag.NewFlagSet(group, pflag.ContinueOnError)
			}
			sets[group].AddFlag(f)
		}
	}
	cmd.LocalFlags().VisitAll(add("Flags"))
	cmd.InheritedFlags().VisitAll(add("Global Flags"))

	var b strings.Builder
	for _, group := range append(flagGroupOrder, "Flags", "Global Flags") {
		if fs := sets[group]; fs != nil {
			fmt.Fprintf(&b, "\n\n%s:\n%s", group, strings.TrimRight(fs.FlagUsages(), " \t\n"))
		}
	}
	return b.String()
}
