package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"

	"github.com/spf13/cobra"
	"github.com/wandb/wsm/cmd/wsm/deploy"
	"github.com/wandb/wsm/pkg/configmaps"
	"github.com/wandb/wsm/pkg/crd"
	"github.com/wandb/wsm/pkg/deployer"
	"github.com/wandb/wsm/pkg/helm"
	"github.com/wandb/wsm/pkg/helm/values"
	"github.com/wandb/wsm/pkg/images"
	"github.com/wandb/wsm/pkg/spec"
	"github.com/wandb/wsm/pkg/term/pkgm"
	"github.com/wandb/wsm/pkg/utils"
	"gopkg.in/yaml.v3"
)

func RootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "wsm",
	}

	return cmd
}

func downloadChartImages(
	url string,
	name string,
	version string,
	vals map[string]interface{},
) ([]string, error) {
	chartsDir := "bundle/charts"
	if err := os.MkdirAll(chartsDir, 0755); err != nil {
		return nil, err
	}

	chart, err := helm.DownloadChart(
		url,
		name,
		version,
		chartsDir,
	)
	if err != nil {
		return nil, err
	}

	runs, err := helm.GetRuntimeObjects(chart, vals)
	if err != nil {
		return nil, err
	}
	return helm.ExtractImages(runs), nil
}

func DownloadCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "download",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("Downloading operator helm chart")
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

			fmt.Println("Downloading wandb helm chart")
			wandbImgs, _ := downloadChartImages(
				spec.Chart.URL,
				spec.Chart.Name,
				spec.Chart.Version,
				spec.Values,
			)

			imgs := utils.RemoveDuplicates(append(wandbImgs, operatorImgs...))
			if len(imgs) == 0 {
				fmt.Println("No images to download.")
				os.Exit(1)
			}

			yamlData, err := yaml.Marshal(spec)
			if err != nil {
				panic(err)
			}
			if err = os.WriteFile("bundle/spec.yaml", yamlData, 0644); err != nil {
				panic(err)
			}

			cb := func(pkg string) {
				path := "bundle/images/" + pkg
				os.MkdirAll(path, 0755)
				err := images.Download(pkg, path+"/image.tgz")
				if err != nil {
					fmt.Println(err)
				}
			}

			if _, err := pkgm.New(imgs, cb).Run(); err != nil {
				fmt.Println("Error deploying:", err)
				os.Exit(1)
			}
		},
	}

	return cmd
}

func base64EncodeFile(filePath string) (string, error) {

	fileContents, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(fileContents), nil
}

func deployOperator(chartsDir string, wandbChartPath string, operatorChartPath string, namespace string, releaseName string, airgapped bool) error {
	if operatorChartPath == "" {
		operatorChartPath = deploy.DownloadHelmChart(
			helm.WandbHelmRepoURL, helm.WandbOperatorChart, "", chartsDir,
		)
	}

	operatorChart := deploy.LoadChart(operatorChartPath)
	if operatorChart == nil {
		return errors.New("could not find operator chart")
	}

	operatorValues := values.Values{}
	if airgapped {
		operatorValues.SetValue("airgapped", true)

		if wandbChartPath == "" {
			wandbChartPath = deploy.DownloadHelmChart(
				helm.WandbHelmRepoURL, helm.WandbChart, "", chartsDir,
			)
		}

		wandbChartBinary, err := base64EncodeFile(wandbChartPath)
		if err != nil {
			return err
		}

		configmaps.UpsertConfigMap(map[string]string{
			helm.WandbChart: wandbChartBinary,
		}, "wandb-charts", namespace)
	}

	deploy.DeployChart(namespace, releaseName, operatorChart, operatorValues.AsMap())

	return nil
}

func specFromBundle(bundlePath string) (*spec.Spec, error) {
	specPath := path.Join(bundlePath, "spec.yaml")
	specData, err := os.ReadFile(specPath)
	if err != nil {
		return nil, err
	}

	spec := &spec.Spec{}
	if err := yaml.Unmarshal(specData, spec); err != nil {
		return nil, err
	}

	return spec, nil
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
			os.MkdirAll(chartsDir, 0755)

			vals := specToApply.Values
			if localVals, err := values.FromYAMLFile(valuesPath); err == nil {
				if finalVals, err := vals.Merge(localVals); err != nil {
					vals = finalVals
				}
			}

			if deployWithHelm {
				if chartPath == "" {
					fmt.Println("Downloading W&B chart from", specToApply.Chart.URL)
					chartPath = deploy.DownloadHelmChart(
						specToApply.Chart.URL, specToApply.Chart.Name, specToApply.Chart.Version, chartsDir)
				}
				chart := deploy.LoadChart(chartPath)
				if _, err := json.Marshal(vals.AsMap()); err != nil {
					panic(err)
				}
				deploy.DeployChart(namespace, releaseName, chart, vals.AsMap())
				os.Exit(0)
			}

			if err := deployOperator(chartsDir, chartPath, operatorChartPath, namespace, "operator", airgapped); err != nil {
				fmt.Println("Error deploying operator:", err)
				os.Exit(1)
			}

			chart := getChart(airgapped, chartPath, specToApply)
			wb := crd.NewWeightsAndBiases(chart, vals)

			if err := crd.ApplyWeightsAndBiases(wb); err != nil {
				fmt.Println("Error applying weightsandbiases:", err)
				os.Exit(1)
			}

			os.Exit(0)
		},
	}

	cmd.Flags().BoolVarP(&deployWithHelm, "helm", "", false, "Deploy the system using the helm (not recommended).")
	cmd.Flags().StringVarP(&bundlePath, "bundle", "b", "", "Path to the bundle to deploy with.")
	cmd.Flags().StringVarP(&valuesPath, "values", "v", "", "Values file to apply to the helm chart yaml.")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "wandb", "Namespace to deploy into.")
	cmd.Flags().StringVarP(&chartPath, "chart", "c", "", "Path to W&B helm chart.")
	cmd.Flags().StringVarP(&operatorChartPath, "operator-chart", "o", "", "Path to operator helm chart.")
	cmd.Flags().BoolVarP(&airgapped, "airgapped", "a", false, "Deploy in airgapped mode.")

	cmd.Flags().MarkHidden("helm")

	return cmd
}

func main() {
	ctx := context.Background()
	cmd := RootCmd()
	cmd.AddCommand(DownloadCmd())
	cmd.AddCommand(DeployCmd())
	cmd.ExecuteContext(ctx)
}
