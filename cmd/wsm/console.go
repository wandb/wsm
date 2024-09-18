package main

import (
	b64 "encoding/base64"
	urlutil "net/url"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"
	"github.com/wandb/wsm/pkg/kubectl"
)

func init() {
	rootCmd.AddCommand(ConsoleCmd())
}

func openBrowser(url string) error {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "windows":
		cmd = "cmd"
		args = []string{"/c", "start"}
	case "darwin":
		cmd = "open"
	default: // "linux", "freebsd", "openbsd", "netbsd"
		cmd = "xdg-open"
	}
	args = append(args, url)
	return exec.Command(cmd, args...).Start()
}

func ConsoleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "console",
		Run: func(cmd *cobra.Command, args []string) {
			pwd, err := kubectl.GetSecret("wandb-password", "default")
			if err != nil {
				panic(err)
			}

			url := "http://localhost:8082/console/login?password=" + urlutil.QueryEscape(
				b64.StdEncoding.EncodeToString([]byte(pwd)),
			)

			time.AfterFunc(500*time.Millisecond, func() {
				_ = openBrowser(url)
			})
			portForward := exec.Command("kubectl", "port-forward", "service/wandb-console", "8082:8082")
			portForward.Stderr = os.Stderr
			portForward.Stdout = os.Stdout
			portForward.Stdin = os.Stdin
			_ = portForward.Run()
		},
	}

	return cmd
}
