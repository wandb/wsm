package utils

import (
	"fmt"
	"strings"

	semverlib "github.com/Masterminds/semver/v3"
)

// EnsureWandbSemverCompatibleImages processes a list of container image references and ensures
// that all Weights & Biases (wandb/*) images use clean semantic versioning tags.
//
// The function:
//  1. Only processes images with the "wandb/" prefix
//  2. Extracts the base semantic version by removing any pre-release identifiers (after "-")
//     or build metadata (after "+")
//  3. Validates that the extracted version is a valid semantic version
//  4. Returns the image with only the major.minor.patch components of the version
//
// For example:
// - "wandb/server:0.9.1-daily.123" becomes "wandb/server:0.9.1"
// - "wandb/server:0.9.1+build.123" becomes "wandb/server:0.9.1"
// - "wandb/server:0.9.1-beta.1+build.123" becomes "wandb/server:0.9.1"
// - Non-wandb images like "nginx:1.19.3-alpine" remain unchanged
// - Invalid semver tags remain unchanged
//
// This ensures consistent versioning for air-gapped deployments and compatibility checks.
func EnsureWandbSemverCompatibleImages(images []string) []string {
	result := make([]string, len(images))
	for i, img := range images {
		if !strings.HasPrefix(img, "wandb/") {
			result[i] = img
			continue
		}

		parts := strings.Split(img, ":")
		if len(parts) != 2 {
			result[i] = img
			continue
		}

		repo, tag := parts[0], parts[1]

		baseVersion := tag
		if idx := strings.IndexAny(tag, "-+"); idx > 0 {
			baseVersion = tag[:idx]
		}

		v, err := semverlib.NewVersion(baseVersion)
		if err == nil {
			result[i] = fmt.Sprintf("%s:%d.%d.%d", repo, v.Major(), v.Minor(), v.Patch())
		} else {
			result[i] = img
		}
	}
	return result
}
