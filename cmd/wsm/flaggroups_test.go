package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// findCommand resolves a command path like "deploy-v2 wandb deploy" from rootCmd.
func findCommand(t *testing.T, path string) *cobra.Command {
	t.Helper()
	cmd, _, err := rootCmd.Find(strings.Fields(path))
	if err != nil || cmd.CommandPath() != "wsm "+path {
		t.Fatalf("command %q not found (got %v, err %v)", path, cmd.CommandPath(), err)
	}
	return cmd
}

func usageOf(t *testing.T, cmd *cobra.Command) string {
	t.Helper()
	// Cobra adds -h/--help lazily at execution; add it so usage matches real --help.
	cmd.InitDefaultHelpFlag()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	defer cmd.SetOut(nil)
	if err := cmd.Usage(); err != nil {
		t.Fatalf("usage: %v", err)
	}
	return buf.String()
}

func TestUsageTemplateGroupsFlags(t *testing.T) {
	if !strings.Contains(rootCmd.UsageTemplate(), "groupedFlagUsages") {
		t.Fatal("usage template was not patched; Cobra's default Flags section changed, update defaultFlagsTemplate")
	}

	out := usageOf(t, findCommand(t, "deploy-v2 wandb deploy"))
	for _, want := range []string{flagGroupTarget + ":", flagGroupInstance + ":", flagGroupNotifications + ":", flagGroupAdvanced + ":"} {
		if !strings.Contains(out, want) {
			t.Errorf("wandb deploy help is missing group %q", want)
		}
	}
	if strings.Index(out, flagGroupTarget+":") > strings.Index(out, flagGroupAdvanced+":") {
		t.Error("groups are not printed in flagGroupOrder")
	}
}

func TestUngroupedCommandKeepsDefaultSections(t *testing.T) {
	out := usageOf(t, findCommand(t, "version"))
	if !strings.Contains(out, "\nFlags:\n") {
		t.Errorf("ungrouped command lost its Flags section:\n%s", out)
	}
}

func TestCRFlagsScopedToDeployCommands(t *testing.T) {
	for _, path := range []string{"deploy-v2 wandb deploy", "deploy-v2 operator"} {
		if findCommand(t, path).Flags().Lookup("cr-set") == nil {
			t.Errorf("%s should have --cr-set", path)
		}
	}
	for _, path := range []string{"deploy-v2 wandb destroy", "deploy-v2 wandb get-ca-cert", "deploy-v2 wandb list-versions", "deploy-v2 operator destroy"} {
		cmd := findCommand(t, path)
		if cmd.Flags().Lookup("cr-set") != nil || cmd.InheritedFlags().Lookup("cr-set") != nil {
			t.Errorf("%s should not have the CR flags", path)
		}
		if cmd.InheritedFlags().Lookup("context") == nil {
			t.Errorf("%s should still inherit --context", path)
		}
	}
}

func TestCRFlagsMutuallyExclusive(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{name: "license only", args: []string{"--license", "x"}},
		{name: "license and file", args: []string{"--license", "x", "--license-file", "y"}, wantErr: true},
		{name: "smtp fields together", args: []string{"--smtp-host", "h", "--smtp-port", "25"}},
		{name: "sink and smtp", args: []string{"--email-sink", "s:k", "--smtp-port", "25"}, wantErr: true},
		{name: "proxy url and secret", args: []string{"--proxy-https-url", "http://p:3128", "--proxy-https-secret", "s:k"}, wantErr: true},
		{name: "http and https proxy", args: []string{"--proxy-http-url", "http://p:3128", "--proxy-https-secret", "s:k"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{Use: "test"}
			addWandbCRFlags(cmd)
			if err := cmd.ParseFlags(tt.args); err != nil {
				t.Fatalf("parse: %v", err)
			}
			err := cmd.ValidateFlagGroups()
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateFlagGroups() err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateCRFlagDependencies(t *testing.T) {
	tests := []struct {
		name                  string
		f                     wandbCRFlags
		createAwsIngressClass bool
		wantErr               bool
	}{
		{name: "defaults", f: wandbCRFlags{createCA: true}},
		{name: "aws ingress class with ingress class", f: wandbCRFlags{createCA: true, ingressClass: "alb"}, createAwsIngressClass: true},
		{name: "aws ingress class without ingress class", f: wandbCRFlags{createCA: true}, createAwsIngressClass: true, wantErr: true},
		{name: "issuer with create-ca=false", f: wandbCRFlags{issuerName: "corp"}},
		{name: "issuer with create-ca=true", f: wandbCRFlags{createCA: true, issuerName: "corp"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCRFlagDependencies(tt.f, tt.createAwsIngressClass)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
