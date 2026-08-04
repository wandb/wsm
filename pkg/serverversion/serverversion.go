// Package serverversion resolves and validates W&B server versions against the
// tags published to the wandb/local Docker Hub repository.
package serverversion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"time"

	"github.com/Masterminds/semver/v3"
)

// Image is the Docker Hub repository holding W&B server releases.
const Image = "wandb/local"

// maxServerMajor drops wandb/local's build-number tags (e.g. "703437737.0.0"),
// which parse as valid semver but are not releases.
const maxServerMajor = 100

// releaseCut matches the pre-release form of a release candidate, e.g.
// "0.83.0-rc.1784580044". Only plain releases and cuts are kept.
var releaseCut = regexp.MustCompile(`^rc\.\d+$`)

// Available returns published server versions at or above min (compared on core
// major.minor.patch), excluding "latest" and non-release build tags, sorted descending.
func Available(ctx context.Context, min string) ([]*semver.Version, error) {
	minV, err := semver.NewVersion(min)
	if err != nil {
		return nil, fmt.Errorf("minimum version %q is not valid semver: %w", min, err)
	}

	tags, err := listTags(ctx, Image)
	if err != nil {
		return nil, err
	}

	var versions []*semver.Version
	for _, tag := range tags {
		v, err := semver.NewVersion(tag)
		if err != nil {
			continue
		}
		if v.Major() > maxServerMajor {
			continue
		}
		if pre := v.Prerelease(); pre != "" && !releaseCut.MatchString(pre) {
			continue
		}
		if core(v).LessThan(minV) {
			continue
		}
		versions = append(versions, v)
	}

	sort.Sort(sort.Reverse(semver.Collection(versions)))
	return versions, nil
}

// CheckFloor rejects a version below min. The floor is compared on the core
// major.minor.patch, so a pre-release of the min line (e.g. 0.80.0-rc.1) passes.
func CheckFloor(version, min string) error {
	v, err := semver.NewVersion(version)
	if err != nil {
		return fmt.Errorf("wandb version %q is not valid semver: %w", version, err)
	}
	minV := semver.MustParse(min)
	if core(v).LessThan(minV) {
		return fmt.Errorf("wandb version %q is below the minimum supported version %s", version, min)
	}
	return nil
}

// CheckAvailable rejects a version that is not a published wandb/local tag.
func CheckAvailable(ctx context.Context, version, min string) error {
	want, err := semver.NewVersion(version)
	if err != nil {
		return fmt.Errorf("wandb version %q is not valid semver: %w", version, err)
	}

	versions, err := Available(ctx, min)
	if err != nil {
		return fmt.Errorf("could not verify wandb version %q against published %s tags: %w", version, Image, err)
	}

	for _, v := range versions {
		if v.Equal(want) {
			return nil
		}
	}
	return fmt.Errorf("wandb version %q is not a published %s tag; run `wsm deploy-v2 wandb list-versions` to see available versions", version, Image)
}

func core(v *semver.Version) *semver.Version {
	return semver.New(v.Major(), v.Minor(), v.Patch(), "", "")
}

const (
	dockerHubBase = "https://registry.hub.docker.com/v2/repositories"
	tagPageSize   = 100
	maxTagPages   = 100 // guard against a runaway "next" cursor
	requestLimit  = 30 * time.Second
)

// listTags returns every tag for a Docker Hub repository, following pagination.
func listTags(ctx context.Context, repository string) ([]string, error) {
	client := &http.Client{Timeout: requestLimit}
	url := fmt.Sprintf("%s/%s/tags/?page_size=%d", dockerHubBase, repository, tagPageSize)

	var tags []string
	for page := 0; url != "" && page < maxTagPages; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetching tags for %s: %w", repository, err)
		}

		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetching tags for %s: %s", repository, resp.Status)
		}

		var p struct {
			Next    string `json:"next"`
			Results []struct {
				Name string `json:"name"`
			} `json:"results"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, fmt.Errorf("parsing tags for %s: %w", repository, err)
		}
		for _, r := range p.Results {
			tags = append(tags, r.Name)
		}
		url = p.Next
	}

	return tags, nil
}
