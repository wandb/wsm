package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"

	"github.com/Masterminds/semver/v3"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
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

type model struct {
	spinner spinner.Model
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.spinner, cmd = m.spinner.Update(msg)
	return m, cmd
}

func (m model) View() string {
	return m.spinner.View()
}

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

func ListCmd() *cobra.Command {
	var platform string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List images required for deployment",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("14")).Render("📦 Starting the process to list all images required for deployment..."))

			// Initialize spinner
			sp := spinner.New()
			sp.Spinner = spinner.Dot
			sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
			m := model{spinner: sp}
			p := tea.NewProgram(m)

			// Run spinner in a separate goroutine
			go func() {
				if _, err := p.Run(); err != nil {
					fmt.Println("Error running spinner:", err)
				}
			}()

			operatorTag, err := getMostRecentTag("wandb/controller")
			if err != nil {
				fmt.Printf("Error fetching the latest operator-wandb controller tag: %v\n", err)
				p.Quit()
				return
			}
			operatorImgs, _ := downloadChartImages(
				helm.WandbHelmRepoURL,
				helm.WandbOperatorChart,
				"", // empty version means latest
				map[string]interface{}{
					"image": map[string]interface{}{
						"tag": operatorTag,
					},
				},
			)

			spec, err := deployer.GetChannelSpec("")
			if err != nil {
				panic(err)
			}

			// Enable weave-trace in the chart values
			if weaveTrace, ok := spec.Values["weave-trace"]; ok {
				weaveTrace.(map[string]interface{})["install"] = true
			}

			wandbImgs, _ := downloadChartImages(
				spec.Chart.URL,
				spec.Chart.Name,
				spec.Chart.Version,
				spec.Values,
			)

			// Stop spinner
			p.Quit()

			// Print images
			fmt.Println(listStyle.Render("Operator Images:"))
			for _, img := range utils.RemoveDuplicates(operatorImgs) {
				fmt.Println(itemStyle.Render(img))
			}

			fmt.Println(listStyle.Render("W&B Images:"))
			for _, img := range utils.RemoveDuplicates(wandbImgs) {
				fmt.Println(itemStyle.Render(img))
			}

			fmt.Println(footerStyle.Render("Here are the images required to deploy W&B. Please ensure these images are available in your internal container registry and update the values.yaml accordingly.\n"))
		},
	}

	cmd.Flags().StringVarP(&platform, "platform", "p", "linux/amd64", "Platform to list images for")

	return cmd
}

func init() {
	rootCmd.AddCommand(ListCmd())
}
