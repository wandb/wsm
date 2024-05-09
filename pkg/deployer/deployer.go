package deployer

import (
	"encoding/json"
	"io"
	"net/http"
	"os"

	"github.com/wandb/wsm/pkg/spec"
)

const DeployerAPI = "https://deploy.wandb.ai/api/v1/operator/channel"

func GetURL() string {
	if v := os.Getenv("DEPLOYER_CHANNEL_URL"); v != "" {
		return v
	}
	return DeployerAPI
}

func GetChannelSpec(license string) (*spec.Spec, error) {
	url := GetURL()
	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	if license != "" {
		req.SetBasicAuth("license", license)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	resBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	spec := new(spec.Spec)
	err = json.Unmarshal(resBody, &spec)
	if err != nil {
		return nil, err
	}

	return spec, nil
}
