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
	spinner      spinner.Model
	operatorImgs []string
	wandbImgs    []string
	err          error
	quitting     bool
}

type fetchCompleteMsg struct {
	operatorImgs []string
	wandbImgs    []string
	err          error
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		fetchImagesCmd(),
	)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case fetchCompleteMsg:
		m.operatorImgs = msg.operatorImgs
		m.wandbImgs = msg.wandbImgs
		m.err = msg.err
		m.quitting = true
		return m, tea.Quit
	}

	return m, nil
}

func (m model) View() string {
	if m.quitting {
		return ""
	}
	return m.spinner.View() + " Loading images..."
}

func fetchImagesCmd() tea.Cmd {
	return func() tea.Msg {
		operatorTag, err := getMostRecentTag("wandb/controller")
		if err != nil {
			return fetchCompleteMsg{
				err: fmt.Errorf("error fetching the latest operator-wandb controller tag: %v", err),
			}
		}

		operatorImgs, err := downloadChartImages(
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
			return fetchCompleteMsg{
				err: fmt.Errorf("error downloading operator chart images: %v", err),
			}
		}

		spec, err := deployer.GetChannelSpec("")
		if err != nil {
			return fetchCompleteMsg{
				err: fmt.Errorf("error getting channel spec: %v", err),
			}
		}

		// Enable weave-trace in the chart values
		if weaveTrace, ok := spec.Values["weave-trace"]; ok {
			weaveTrace.(map[string]interface{})["install"] = true
		}

		wandbImgs, err := downloadChartImages(
			spec.Chart.URL,
			spec.Chart.Name,
			spec.Chart.Version,
			spec.Values,
		)
		if err != nil {
			return fetchCompleteMsg{
				err: fmt.Errorf("error downloading W&B chart images: %v", err),
			}
		}

		return fetchCompleteMsg{
			operatorImgs: utils.RemoveDuplicates(operatorImgs),
			wandbImgs:    utils.RemoveDuplicates(wandbImgs),
			err:          nil,
		}
	}
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

			// Initialize the spinner
			s := spinner.New()
			s.Spinner = spinner.Dot
			s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))

			// Create initial model
			m := model{
				spinner: s,
			}

			// Run the program with proper terminal handling
			p := tea.NewProgram(m)
			finalModel, err := p.Run()
			if err != nil {
				fmt.Println("Error running program:", err)
				return
			}

			// Extract results from the final model
			finalState := finalModel.(model)
			if finalState.err != nil {
				fmt.Printf("%v\n", finalState.err)
				return
			}

			// Print images with proper formatting
			fmt.Println(listStyle.Render("Operator Images:"))
			for _, img := range finalState.operatorImgs {
				fmt.Println(itemStyle.Render(img))
			}

			fmt.Println(listStyle.Render("W&B Images:"))
			for _, img := range finalState.wandbImgs {
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
