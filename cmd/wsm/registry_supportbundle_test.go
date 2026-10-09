package main

import (
	"testing"

	"github.com/wandb/wsm/pkg/observabaility/supportbundle"
)

func TestSupportBundleMirrorItems(t *testing.T) {
	items := supportBundleMirrorItems("registry.corp:5000/mirror")
	want := map[string]string{
		"us-docker.pkg.dev/wandb-production/public/wandb/lumen:" + supportbundle.DefaultLumenVersion: "registry.corp:5000/mirror/wandb/lumen:" + supportbundle.DefaultLumenVersion,
		supportbundle.VictoriaHelperImage: "registry.corp:5000/mirror/" + supportbundle.VictoriaHelperImage,
	}
	if len(items) != len(want) {
		t.Fatalf("got %d items, want %d", len(items), len(want))
	}
	for _, it := range items {
		if want[it.src] != it.dst {
			t.Errorf("%s -> %s, want %s", it.src, it.dst, want[it.src])
		}
	}
}
