package deploy

import (
	"time"

	"github.com/wandb/wsm/pkg/helm"
	"github.com/wandb/wsm/pkg/term/task"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
)

func DownloadHelmChart(
	url string,
	name string,
	version string,
	dest string,
) string {
	chart, err := helm.DownloadChart(
		url,
		name,
		version,
		dest,
	)
	if err != nil {
		panic(err)
	}
	return chart
}

func LoadChart(chartPath string) *chart.Chart {
	chart, err := loader.Load(chartPath)
	if err != nil {
		panic(err)
	}
	return chart
}

func DeployChart(
	namespace string,
	releaseName string,
	chart *chart.Chart,
	vals map[string]interface{},
) {
	cb := func() error {
		_, err := helm.Apply(namespace, releaseName, chart, vals)
		time.Sleep(5 * time.Second)
		return err
	}
	if _, err := task.New("Deploying wandb", cb).Run(); err != nil {
		panic(err)
	}
}
