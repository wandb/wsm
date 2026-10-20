package supportbundle

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const listOutput = `{
  "version": "v1",
  "bundles": [
    {
      "location": "s3://bucket/agent/service-bundles/support-bundle-2026-10-19T15_00_00Z.tar.gz",
      "sizeBytes": 2048,
      "lastModified": "2026-10-19T15:00:05Z",
      "metadata": {"wandb-support-bundle": "true", "wandb-support-bundle-id": "sb-test", "wandb-expires-at": "2026-10-20T15:00:05Z"}
    },
    {
      "location": "s3://bucket/agent/service-bundles/old.tar.gz",
      "sizeBytes": 10,
      "lastModified": "2026-10-01T00:00:00Z",
      "metadata": {"wandb-support-bundle": "true"}
    }
  ]
}
`

func fakeSession(executor Executor) *Session {
	return &Session{
		executor:  executor,
		Job:       &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "support-bundle-list-abc123", Namespace: "wandb"}},
		Pod:       "support-bundle-list-abc123-pod",
		Container: "collector",
		Image:     "lumen:0.4.10",
	}
}

func TestParseList(t *testing.T) {
	result, err := ParseList([]byte(listOutput))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Bundles) != 2 || string(result.Raw) != listOutput {
		t.Fatalf("result = %+v", result)
	}
	first := result.Bundles[0]
	if first.ID() != "sb-test" || first.SizeBytes != 2048 {
		t.Errorf("first bundle = %+v", first)
	}
	if result.Bundles[1].ID() != "" {
		t.Error("bundle without ID metadata reported an ID")
	}

	empty, err := ParseList([]byte(`{"version":"v1","bundles":[]}`))
	if err != nil || len(empty.Bundles) != 0 {
		t.Errorf("empty list = %+v, %v", empty, err)
	}
	if _, err := ParseList([]byte(`{"version":"v2"}`)); err == nil {
		t.Error("unknown version accepted")
	}
	if _, err := ParseList([]byte(`{"version":"v1","failure":{"stage":"List","reason":"ListFailed","message":"denied"}}`)); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Errorf("failure report: err = %v", err)
	}
	if _, err := ParseList([]byte("not json")); err == nil {
		t.Error("garbage accepted")
	}
}

func TestListPassesMetadataFilters(t *testing.T) {
	executor := &fakeExecutor{stdout: `{"version":"v1","bundles":[]}`}
	if _, err := fakeSession(executor).List(context.Background(), map[string]string{BundleIDMetadata: "sb-test"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"bundle-manager", "list", "--metadata", "wandb-support-bundle-id=sb-test"}
	if !reflect.DeepEqual(executor.commands[0], want) {
		t.Errorf("command = %v, want %v", executor.commands[0], want)
	}
}

func TestResolve(t *testing.T) {
	ctx := context.Background()
	one := `{"version":"v1","bundles":[{"location":"s3://bucket/agent/service-bundles/a.tar.gz","sizeBytes":1,"metadata":{"wandb-support-bundle-id":"sb-test"}}]}`
	two := `{"version":"v1","bundles":[{"location":"s3://bucket/agent/service-bundles/a.tar.gz"},{"location":"s3://bucket/agent/service-bundles/b.tar.gz"}]}`
	none := `{"version":"v1","bundles":[]}`

	bundle, err := fakeSession(&fakeExecutor{stdout: one}).Resolve(ctx, "sb-test")
	if err != nil || bundle.ID() != "sb-test" {
		t.Errorf("by ID: %+v, %v", bundle, err)
	}

	if _, err := fakeSession(&fakeExecutor{stdout: none}).Resolve(ctx, "sb-test"); !errors.Is(err, ErrBundleNotFound) {
		t.Errorf("missing ID: err = %v", err)
	}

	_, err = fakeSession(&fakeExecutor{stdout: two}).Resolve(ctx, "sb-test")
	if err == nil || !strings.Contains(err.Error(), "b.tar.gz") || errors.Is(err, ErrBundleNotFound) {
		t.Errorf("ambiguous ID: err = %v", err)
	}

	executor := &fakeExecutor{stdout: listOutput}
	bundle, err = fakeSession(executor).Resolve(ctx, "s3://bucket/agent/service-bundles/support-bundle-2026-10-19T15_00_00Z.tar.gz")
	if err != nil || bundle.ID() != "sb-test" {
		t.Errorf("by location: %+v, %v (the location must map back to its ID)", bundle, err)
	}
	if got := executor.commands[0]; !reflect.DeepEqual(got, []string{"bundle-manager", "list"}) {
		t.Errorf("location lookup must list everything, ran %v", got)
	}

	if _, err := fakeSession(&fakeExecutor{stdout: listOutput}).Resolve(ctx, "s3://bucket/agent/service-bundles/missing.tar.gz"); !errors.Is(err, ErrBundleNotFound) {
		t.Errorf("unknown location: err = %v", err)
	}

	if _, err := fakeSession(&fakeExecutor{}).Resolve(ctx, "--location=x"); err == nil {
		t.Error("flag-like ID accepted")
	}
}

func TestEntriesAndSort(t *testing.T) {
	result, err := ParseList([]byte(listOutput))
	if err != nil {
		t.Fatal(err)
	}
	running := []batchv1.Job{
		{ObjectMeta: metav1.ObjectMeta{Name: "sb-test", CreationTimestamp: metav1.Now()}},
		{ObjectMeta: metav1.ObjectMeta{Name: "support-bundle-new", CreationTimestamp: metav1.Now()}},
	}
	entries := Entries(result.Bundles, running)
	if len(entries) != 3 {
		t.Fatalf("entries = %+v; an uploaded ID must not also show as collecting", entries)
	}

	if err := SortEntries(entries, "created", ""); err != nil {
		t.Fatal(err)
	}
	if entries[0].ID != "support-bundle-new" || entries[0].Status != EntryCollecting || entries[2].Location != "s3://bucket/agent/service-bundles/old.tar.gz" {
		t.Errorf("by created = %+v", entries)
	}
	if err := SortEntries(entries, "size", ""); err != nil || entries[0].SizeBytes != 2048 {
		t.Errorf("by size = %+v, %v", entries, err)
	}
	if err := SortEntries(entries, "id", ""); err != nil || entries[0].ID != "" || entries[1].ID != "sb-test" {
		t.Errorf("by id = %+v, %v", entries, err)
	}
	if err := SortEntries(entries, "status", ""); err != nil || entries[0].Status != EntryCollecting || entries[1].Status != EntryUploaded {
		t.Errorf("by status = %+v, %v", entries, err)
	}
	if err := SortEntries(entries, "status", "descending"); err != nil || entries[0].Status != EntryUploaded || entries[1].Location != "s3://bucket/agent/service-bundles/old.tar.gz" {
		t.Errorf("by status descending = %+v, %v", entries, err)
	}
	if err := SortEntries(entries, "created", "ascending"); err != nil || entries[0].Location != "s3://bucket/agent/service-bundles/old.tar.gz" {
		t.Errorf("by created ascending = %+v, %v", entries, err)
	}
	if err := SortEntries(entries, "id", "desc"); err != nil || entries[0].ID != "support-bundle-new" {
		t.Errorf("by id descending = %+v, %v", entries, err)
	}
	if err := SortEntries(entries, "nope", ""); err == nil {
		t.Error("unknown sort key accepted")
	}
	if err := SortEntries(entries, "id", "sideways"); err == nil {
		t.Error("unknown sort order accepted")
	}
}

func TestStatusJSONKeepsShapeAfterJobRemoval(t *testing.T) {
	decode := func(status *CollectionStatus) map[string]any {
		t.Helper()
		raw, err := json.Marshal(status)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	live := describe(testCollectionJob(complete), collectionPod(exited(validResult, 0)))
	live.ID = "sb-test"
	gone := StoredStatus("sb-test", Bundle{Location: "s3://bucket/agent/service-bundles/a.tar.gz", SizeBytes: 1})

	for name, out := range map[string]map[string]any{"live": decode(live), "gone": decode(gone)} {
		if out["id"] != "sb-test" || out["phase"] == nil {
			t.Errorf("%s status JSON = %v, want id and phase", name, out)
		}
	}
	if stored, _ := decode(gone)["stored"].(map[string]any); stored["location"] != "s3://bucket/agent/service-bundles/a.tar.gz" || decode(gone)["phase"] != PhaseUnknown {
		t.Errorf("gone status JSON = %v", decode(gone))
	}
}
