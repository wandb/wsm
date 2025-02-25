package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"github.com/wandb/wsm/pkg/deployer"
	"github.com/wandb/wsm/pkg/helm"
	"github.com/wandb/wsm/pkg/utils"
)

var (
	listStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("12")).
			Bold(true)

	itemStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("10")).
			PaddingLeft(2)

	footerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("14")).
			Bold(true)
)

// Function to fetch the latest tag from Docker Hub API
func getMostRecentTag(repository string) (string, error) {
	url := fmt.Sprintf("https://registry.hub.docker.com/v2/repositories/%s/tags/", repository)
	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("error fetching tags: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response body: %v", err)
	}

	// Parse the JSON response
	var result map[string]interface{}
	err = json.Unmarshal(body, &result)
	if err != nil {
		return "", fmt.Errorf("error unmarshalling JSON: %v", err)
	}

	// Extract tags and filter out "latest"
	var tags []*semver.Version
	if results, ok := result["results"].([]interface{}); ok {
		for _, r := range results {
			if tag, ok := r.(map[string]interface{})["name"].(string); ok && tag != "latest" {
				version, err := semver.NewVersion(tag)
				if err == nil {
					tags = append(tags, version)
				}
			}
		}
	}

	// Sort the tags in descending order
	sort.Sort(sort.Reverse(semver.Collection(tags)))

	// Return the most recent tag
	if len(tags) == 0 {
		return "", fmt.Errorf("no valid tags found")
	}
	return tags[0].String(), nil
}

// SafeConsoleSpinner is a very simple spinner that doesn't mess with terminal state
type SafeConsoleSpinner struct {
	message      string
	stopChan     chan struct{}
	stopped      bool
	spinnerChars []string
	mu           sync.Mutex
}

// NewSpinner creates a new safe console spinner
func NewSpinner(message string) *SafeConsoleSpinner {
	return &SafeConsoleSpinner{
		message:      message,
		stopChan:     make(chan struct{}),
		spinnerChars: []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"},
	}
}

// Start starts the spinner
func (s *SafeConsoleSpinner) Start() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	// Set up signal handling to ensure terminal cleanup on interrupt
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		defer signal.Stop(sigChan)

		i := 0
		for {
			select {
			case <-s.stopChan:
				// The spinner.Stop() function handles cleanup now
				return
			case <-sigChan:
				// Clear the spinner line on interrupt
				fmt.Printf("\r%s\r", spaces(80))
				os.Exit(1)
			default:
				s.mu.Lock()
				if s.stopped {
					s.mu.Unlock()
					return
				}
				fmt.Printf("\r%s %s ", s.spinnerChars[i], s.message)
				s.mu.Unlock()

				i = (i + 1) % len(s.spinnerChars)
				time.Sleep(100 * time.Millisecond)
			}
		}
	}()
}

// Stop stops the spinner
func (s *SafeConsoleSpinner) Stop() {
	s.mu.Lock()
	if !s.stopped {
		s.stopped = true
		close(s.stopChan)
		// Clear the spinner line completely and add a newline
		fmt.Printf("\r%s\r\n", spaces(80))
	}
	s.mu.Unlock()
}

// spaces returns a string of n spaces
func spaces(n int) string {
	s := ""
	for i := 0; i < n; i++ {
		s += " "
	}
	return s
}

func ListCmd() *cobra.Command {
	var platform string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List images required for deployment",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("14")).Render("📦 Starting the process to list all images required for deployment..."))

			// Create a spinner that won't mess with terminal state
			spinner := NewSpinner("Loading images...")
			spinner.Start()

			// Variables to store results
			var operatorImgs, wandbImgs []string

			// Handle cleanup if the command is interrupted
			c := make(chan os.Signal, 1)
			signal.Notify(c, os.Interrupt, syscall.SIGTERM)
			go func() {
				<-c
				spinner.Stop()
				os.Exit(1)
			}()

			// Fetch operator images
			operatorTag, err := getMostRecentTag("wandb/controller")
			if err != nil {
				spinner.Stop()
				fmt.Printf("Error fetching the latest operator-wandb controller tag: %v\n", err)
				return
			}

			operatorImgs, err = downloadChartImages(
				helm.WandbHelmRepoURL,
				helm.WandbOperatorChart,
				"", // empty version means latest
				map[string]interface{}{
					"image": map[string]interface{}{
						"tag": operatorTag,
					},
				},
			)
			if err != nil {
				spinner.Stop()
				fmt.Printf("Error downloading operator chart images: %v\n", err)
				return
			}

			spec, err := deployer.GetChannelSpec("")
			if err != nil {
				spinner.Stop()
				fmt.Printf("Error getting channel spec: %v\n", err)
				return
			}

			// Enable weave-trace in the chart values
			if weaveTrace, ok := spec.Values["weave-trace"]; ok {
				weaveTrace.(map[string]interface{})["install"] = true
			}

			wandbImgs, err = downloadChartImages(
				spec.Chart.URL,
				spec.Chart.Name,
				spec.Chart.Version,
				spec.Values,
			)
			if err != nil {
				spinner.Stop()
				fmt.Printf("Error downloading W&B chart images: %v\n", err)
				return
			}

			// Stop the spinner
			spinner.Stop()

			// Print images (no additional newline needed - spinner.Stop adds one)
			fmt.Println(listStyle.Render("Operator Images:"))
			for _, img := range utils.RemoveDuplicates(operatorImgs) {
				fmt.Println(itemStyle.Render(img))
			}

			fmt.Println(listStyle.Render("W&B Images:"))
			for _, img := range utils.RemoveDuplicates(wandbImgs) {
				fmt.Println(itemStyle.Render(img))
			}

			fmt.Println(footerStyle.Render("Here are the images required to deploy W&B. Please ensure these images are available in your internal container registry and update the values.yaml accordingly."))
		},
	}

	cmd.Flags().StringVarP(&platform, "platform", "p", "linux/amd64", "Platform to list images for")

	return cmd
}

func init() {
	rootCmd.AddCommand(ListCmd())
}
