package utils

import (
	"reflect"
	"testing"
)

func TestEnsureWandbSemverCompatibleImages(t *testing.T) {
	tests := []struct {
		name     string
		images   []string
		expected []string
	}{
		{
			name: "handles standard semver tags",
			images: []string{
				"wandb/server:0.9.1",
			},
			expected: []string{
				"wandb/server:0.9.1",
			},
		},
		{
			name: "cleans daily tags",
			images: []string{
				"wandb/server:0.9.1-daily.123",
			},
			expected: []string{
				"wandb/server:0.9.1",
			},
		},
		{
			name: "ignores non-wandb images",
			images: []string{
				"nginx:1.19.3-daily.1",
				"wandb/server:0.9.1-daily.123",
			},
			expected: []string{
				"nginx:1.19.3-daily.1",
				"wandb/server:0.9.1",
			},
		},
		{
			name: "preserves non-semver tags like 'latest'",
			images: []string{
				"wandb/server:latest",
			},
			expected: []string{
				"wandb/server:latest",
			},
		},
		{
			name: "handles malformed image strings",
			images: []string{
				"wandb/server",
				"wandb/server:0.9.1:extra",
			},
			expected: []string{
				"wandb/server",
				"wandb/server:0.9.1:extra",
			},
		},
		{
			name: "handles pre-release and build metadata",
			images: []string{
				"wandb/server:0.9.1-beta.1+build.123",
				"wandb/server:0.9.1+build.123",
			},
			expected: []string{
				"wandb/server:0.9.1",
				"wandb/server:0.9.1",
			},
		},
		{
			name: "handles registries with ports and semver tags",
			images: []string{
				"localhost:5000/console:2.15.2",
				"localhost:5000/console:2.15.2+build.123",
			},
			expected: []string{
				"localhost:5000/console:2.15.2",
				"localhost:5000/console:2.15.2+build.123",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := EnsureWandbSemverCompatibleImages(tt.images)
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("EnsureWandbSemverCompatibleImages() = %v, want %v", result, tt.expected)
			}
		})
	}
}
