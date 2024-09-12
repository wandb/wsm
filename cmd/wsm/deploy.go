package main

import (
	"encoding/base64"
	"fmt"
	"github.com/pkg/errors"
	"github.com/wandb/wsm/pkg/crd"
	"github.com/wandb/wsm/pkg/deployer"
	"github.com/wandb/wsm/pkg/utils"
	"os"
	"path"

	"github.com/spf13/cobra"
	"github.com/wandb/wsm/pkg/helm"
	"github.com/wandb/wsm/pkg/helm/values"
	"github.com/wandb/wsm/pkg/kubectl"
	"github.com/wandb/wsm/pkg/spec"
	"gopkg.in/yaml.v3"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
)

func init() {
	rootCmd.AddCommand(DeployCmd())
}

var bundlePath string
var chartPath string
var namespace string
var valuesPath string
var airgapped bool
var temporaryDirectory string

var operatorCmd = &cobra.Command{
	Use: "operator",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Deploying operator")
		releaseName := "operator"

		operatorChartPath, err := getChartPath([]string{chartPath, bundlePath}, helm.WandbOperatorChart)

		operatorValues := values.Values{}
		if valuesPath != "" {
			if _, err := os.Stat(valuesPath); os.IsNotExist(err) {
				fmt.Printf("Values file %s does not exist\n", valuesPath)
				os.Exit(1)
			}
		}

		if localVals, err := values.FromYAMLFile(valuesPath); err == nil {
			if _, ok := localVals["wandb"]; ok {
				if operatorValsMap, ok := localVals["operator"]; ok {
					operatorValues = operatorValsMap.(map[string]interface{})
				}
			}

		}

		if airgapped {
			_ = operatorValues.SetValue("airgapped", true)
		}

		operatorChart := loadChart(operatorChartPath)
		_, err = helm.Apply(namespace, releaseName, operatorChart, operatorValues.AsMap())
		if err != nil {
			fmt.Printf("Error deploying operator: %v\n", err)
			os.Exit(1)
		}
	},
}

var chartsCmd = &cobra.Command{
	Use: "chart-cm",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Deploying charts")
		var wandbChartPath string
		var err error

		wandbChartPath, err = getChartPath([]string{chartPath, bundlePath}, helm.WandbChart)

		wandbChartBinary, err := base64EncodeFile(wandbChartPath)
		if err != nil {
			panic(err)
		}

		err = kubectl.UpsertConfigMap(map[string]string{
			helm.WandbChart: wandbChartBinary,
		}, "wandb-charts", namespace)
		if err != nil {
			panic(fmt.Sprintf("Error upserting config map: %v", err))
		}
	},
}

var wandbCRCmd = &cobra.Command{
	Use: "wandb-cr",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Deploying W&B CR")

		getSpec := func() (*spec.Spec, error) {
			if bundlePath != "" {
				return specFromBundle(bundlePath)
			}
			return deployer.GetChannelSpec("")
		}

		specToApply, err := getSpec()
		if err != nil {
			fmt.Println("Error getting spec:", err)
			os.Exit(1)
		}

		vals := specToApply.Values
		if valuesPath != "" {
			if _, err := os.Stat(valuesPath); os.IsNotExist(err) {
				fmt.Printf("Values file %s does not exist\n", valuesPath)
				os.Exit(1)
			}
		}

		if localVals, err := values.FromYAMLFile(valuesPath); err == nil {
			if _, ok := localVals["wandb"]; ok {
				vals, err = values.Values(localVals["wandb"].(map[string]interface{})).Merge(vals)
				if err != nil {
					fmt.Println("Error merging values:", err)
					os.Exit(1)
				}
			}
		}

		helmChart := getChart(airgapped, helm.WandbChart, specToApply)
		wb := crd.NewWeightsAndBiases(helmChart, vals)

		fmt.Println("Applying weights and biases crd")
		if err := crd.ApplyWeightsAndBiases(wb); err != nil {
			fmt.Println("Error applying weightsandbiases:", err)
			fmt.Println("wandb-cr:", wb)
			os.Exit(1)
		}

		fmt.Println("Weights and Biases crd applied, finish setting up your instance by running `wsm console`")
	},
}

func DeployCmd() *cobra.Command {
	//releaseName := "wandb"
	//var operatorChartPath string

	cmd := &cobra.Command{
		Use: "deploy",
		Run: func(cmd *cobra.Command, args []string) {
			if airgapped {
				chartsCmd.Run(cmd, args)
			}
			operatorCmd.Run(cmd, args)
			wandbCRCmd.Run(cmd, args)
		},
	}

	cmd.PersistentFlags().StringVarP(&bundlePath, "bundle", "b", "", "Path to the bundle to deploy with. Cannot be combined with chart.")
	cmd.PersistentFlags().StringVarP(&chartPath, "chart", "c", "", "Path to W&B helm chart. Cannot be combined with bundle.")
	cmd.PersistentFlags().StringVarP(&valuesPath, "values", "v", "", "Values file to apply to the helm chart yaml.")
	cmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "wandb", "Namespace to deploy into.")
	cmd.PersistentFlags().BoolVarP(&airgapped, "airgapped", "a", false, "Deploy in airgapped mode.")

	cmd.AddCommand(operatorCmd)
	cmd.AddCommand(chartsCmd)
	cmd.AddCommand(wandbCRCmd)

	return cmd
}

func getChartPath(searchPaths []string, chart string) (chartPath string, err error) {
	tmpDir := getTmpDir()

	if len(searchPaths) > 0 {
		for _, searchPath := range searchPaths {
			chartPath, err = utils.PathFromDir(searchPath+"/charts", chart)
			if chartPath != "" && err == nil {
				return
			}
		}
		if chartPath == "" {
			err = errors.New(fmt.Sprintf("Error finding %s chart in search paths: %v", chart, searchPaths))
		}
	} else {
		chartPath, err = utils.PathFromDir(tmpDir, chart)
		if err != nil {
			chartPath = downloadHelmChart(helm.WandbHelmRepoURL, chart, "", tmpDir)
		}
	}

	return
}

func getTmpDir() string {
	if temporaryDirectory == "" {
		var err error
		temporaryDirectory, err = os.MkdirTemp(os.TempDir(), "wsm")
		if err != nil {
			panic(err)
		}
	}
	return temporaryDirectory
}

func base64EncodeFile(filePath string) (string, error) {

	fileContents, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(fileContents), nil
}

func downloadHelmChart(url string, name string, version string, dest string) string {
	helmChart, err := helm.DownloadChart(
		url,
		name,
		version,
		dest,
	)
	if err != nil {
		panic(err)
	}
	return helmChart
}

func loadChart(chartPath string) *chart.Chart {
	helmChart, err := loader.Load(chartPath)
	if err != nil {
		panic(err)
	}
	return helmChart
}

func specFromBundle(bundlePath string) (*spec.Spec, error) {
	specPath := path.Join(bundlePath, "spec.yaml")
	specData, err := os.ReadFile(specPath)
	if err != nil {
		return nil, err
	}

	wandbSpec := &spec.Spec{}
	if err := yaml.Unmarshal(specData, wandbSpec); err != nil {
		return nil, err
	}

	return wandbSpec, nil
}

func getChart(airgapped bool, chartPath string, specToApply *spec.Spec) spec.Chart {
	if airgapped {
		return spec.Chart{Path: "/charts/" + chartPath}
	}
	return spec.Chart{
		URL:     specToApply.Chart.URL,
		Version: specToApply.Chart.Version,
		Name:    specToApply.Chart.Name,
	}
}
