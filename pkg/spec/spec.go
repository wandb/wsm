package spec

import "github.com/wandb/wsm/pkg/helm/values"

type Spec struct {
	Chart struct {
		URL     string `json:"url" yaml:"url"`
		Version string `json:"version" yaml:"version"`
		Name    string `json:"name" yaml:"name"`
	} `json:"chart" yaml:"chart"`
	Values values.Values `json:"values" yaml:"values"`
}
