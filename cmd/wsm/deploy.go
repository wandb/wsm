package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"

	"github.com/spf13/cobra"
	"github.com/wandb/wsm/pkg/crd"
	"github.com/wandb/wsm/pkg/deployer"
	"github.com/wandb/wsm/pkg/helm"
	"github.com/wandb/wsm/pkg/helm/values"
	"github.com/wandb/wsm/pkg/kubectl"
	"github.com/wandb/wsm/pkg/spec"
	"github.com/wandb/wsm/pkg/utils"
	"gopkg.in/yaml.v3"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
)

func init() {
	rootCmd.AddCommand(DeployCmd())
}

func base64EncodeFile(filePath string) (string, error) {

	fileContents, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(fileContents), nil
}

func downloadHelmChart(
	url string,
	name string,
	version string,
	dest string,
) string {
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

func deployOperator(chartsDir string, wandbChartPath string, operatorChartPath string, operatorValues values.Values, namespace string, releaseName string, airgapped bool) error {
	fmt.Println("Deploying operator")
	if operatorChartPath == "" {
		operatorChartPath = downloadHelmChart(
			helm.WandbHelmRepoURL, helm.WandbOperatorChart, "", chartsDir,
		)
	}

	operatorChart := loadChart(operatorChartPath)
	if operatorChart == nil {
		return errors.New("could not find operator chart")
	}

	if airgapped {
		_ = operatorValues.SetValue("airgapped", true)

		if wandbChartPath == "" {
			wandbChartPath = downloadHelmChart(
				helm.WandbHelmRepoURL, helm.WandbChart, "", chartsDir,
			)
		}

		wandbChartBinary, err := base64EncodeFile(wandbChartPath)
		if err != nil {
			return err
		}

		err = kubectl.UpsertConfigMap(map[string]string{
			helm.WandbChart: wandbChartBinary,
		}, "wandb-charts", namespace)
		if err != nil {
			panic(fmt.Sprintf("Error upserting config map: %v", err))
		}
	}

	_, err := helm.Apply(namespace, releaseName, operatorChart, operatorValues.AsMap())
	return err
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

type LocalSpec struct {
	Chart struct {
		Path string `json:"path" yaml:"path"`
	} `json:"chart" yaml:"chart"`
	Values values.Values `json:"values" yaml:"values"`
}

func getChart(airgapped bool, chartPath string, specToApply *spec.Spec) spec.Chart {
	if airgapped {
		return spec.Chart{Path: chartPath}
	}
	return spec.Chart{
		URL:     specToApply.Chart.URL,
		Version: specToApply.Chart.Version,
		Name:    specToApply.Chart.Name,
	}
}

func DeployCmd() *cobra.Command {
	var deployWithHelm bool
	var bundlePath string
	var namespace string
	releaseName := "wandb"
	var valuesPath string
	var chartPath string
	var operatorChartPath string
	var airgapped bool

	cmd := &cobra.Command{
		Use: "deploy",
		Run: func(cmd *cobra.Command, args []string) {
			homedir, err := os.UserHomeDir()
			if err != nil {
				fmt.Printf("could not find home dir: %v", err)
				os.Exit(1)
			}

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

			if bundlePath != "" {
				if _, err := os.Stat(bundlePath); os.IsNotExist(err) {
					fmt.Printf("Bundle path %s does not exist\n", bundlePath)
					os.Exit(1)
				}

				chartPath, err = utils.PathFromDir(bundlePath+"/charts", helm.WandbChart)
				if err != nil {
					fmt.Println("Error finding wandb chart:", err)
					os.Exit(1)
				}

				operatorChartPath, err = utils.PathFromDir(bundlePath+"/charts", helm.WandbOperatorChart)
				if err != nil {
					fmt.Println("Error finding operator chart:", err)
					os.Exit(1)
				}
			}

			chartsDir := path.Join(homedir, ".wandb", "charts")
			_ = os.MkdirAll(chartsDir, 0755)

			vals := specToApply.Values
			operatorVals := values.Values{}
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

					if err != nil {
						fmt.Println("Error merging values:", err)
						os.Exit(1)
					}
				}

				if operatorValsMap, ok := localVals["operator"]; ok {
					operatorVals = operatorValsMap.(map[string]interface{})
				}
			}

			if deployWithHelm {
				if chartPath == "" {
					fmt.Println("Downloading W&B chart from", specToApply.Chart.URL)
					chartPath = downloadHelmChart(
						specToApply.Chart.URL, specToApply.Chart.Name, specToApply.Chart.Version, chartsDir)
				}
				helmChart := loadChart(chartPath)
				if _, err := json.Marshal(vals.AsMap()); err != nil {
					panic(err)
				}
				fmt.Println("Deploying helm chart")
				if _, err := helm.Apply(namespace, releaseName, helmChart, vals.AsMap()); err != nil {
					fmt.Println("Error deploying helm chart:", err)
					os.Exit(1)
				}

				fmt.Println("Helm chart deployed, finish setting up your instance by running `wsm console`")
				os.Exit(0)
			}

			if err := deployOperator(chartsDir, chartPath, operatorChartPath, operatorVals, namespace, "operator", airgapped); err != nil {
				fmt.Println("Error deploying operator:", err)
				os.Exit(1)
			}

			helmChart := getChart(airgapped, chartPath, specToApply)
			wb := crd.NewWeightsAndBiases(helmChart, vals)

			fmt.Println("Applying weights and biases crd")
			if err := crd.ApplyWeightsAndBiases(wb); err != nil {
				fmt.Println("Error applying weightsandbiases:", err)
				os.Exit(1)
			}

			fmt.Println("Weights and Biases crd applied, finish setting up your instance by running `wsm console`")
			os.Exit(0)
		},
	}

	cmd.Flags().BoolVarP(&deployWithHelm, "helm", "", false, "Deploy the system using the helm (not recommended).")
	cmd.Flags().StringVarP(&bundlePath, "bundle", "b", "", "Path to the bundle to deploy with.")
	cmd.Flags().StringVarP(&valuesPath, "values", "v", "", "Values file to apply to the helm chart yaml.")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "wandb", "Namespace to deploy into.")
	cmd.Flags().StringVarP(&chartPath, "chart", "c", "", "Path to W&B helm chart.")
	cmd.Flags().BoolVarP(&airgapped, "airgapped", "a", false, "Deploy in airgapped mode.")

	_ = cmd.Flags().MarkHidden("helm")

	return cmd
}
