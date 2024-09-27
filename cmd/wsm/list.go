package main

import (
	"fmt"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbletea"
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
				if err := p.Start(); err != nil {
					fmt.Println("Error running spinner:", err)
				}
			}()

			// Download and list images
			operatorImgs, _ := downloadChartImages(
				helm.WandbHelmRepoURL,
				helm.WandbOperatorChart,
				"", // empty version means latest
				map[string]interface{}{
					"image": map[string]interface{}{
						"tag": "1.10.1",
					},
				},
			)

			spec, err := deployer.GetChannelSpec("")
			if err != nil {
				panic(err)
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
