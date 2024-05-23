package spec

import "github.com/wandb/wsm/pkg/helm/values"

type Chart struct {
	URL     string `json:"url" yaml:"url"`
	Version string `json:"version" yaml:"version"`
	Name    string `json:"name" yaml:"name"`
	Path    string `json:"path" yaml:"path"`
}
type Spec struct {
	Chart  Chart         `json:"chart" yaml:"chart"`
	Values values.Values `json:"values" yaml:"values"`
}
