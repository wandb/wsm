package supportbundle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type memStore struct {
	data      []byte
	etag      string
	failAfter int // bytes served before failing, 0 = never
	offsets   []int64
	deleted   bool
}

func (s *memStore) Stat(context.Context, string) (ObjectInfo, error) {
	return ObjectInfo{Size: int64(len(s.data)), ETag: s.etag}, nil
}

func (s *memStore) ReadFrom(_ context.Context, _ string, offset int64, _ string) (io.ReadCloser, error) {
	s.offsets = append(s.offsets, offset)
	body := s.data[offset:]
	if s.failAfter > 0 {
		n := s.failAfter
		s.failAfter = 0
		return io.NopCloser(io.MultiReader(bytes.NewReader(body[:n]), errReader{})), nil
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func (s *memStore) Delete(context.Context, string) error { s.deleted = true; return nil }

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func artifactFor(data []byte) *Artifact {
	sum := sha256.Sum256(data)
	return &Artifact{Location: "s3://bucket/k.tar.gz", SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
}

func TestDownloadResumesAndVerifies(t *testing.T) {
	data := []byte(strings.Repeat("bundle-bytes-", 100))
	store := &memStore{data: data, etag: `"e1"`, failAfter: 300}
	out := filepath.Join(t.TempDir(), "b.tgz")

	if _, err := Download(context.Background(), store, "k.tar.gz", out, artifactFor(data)); err == nil {
		t.Fatal("first attempt should be interrupted")
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("incomplete download must not appear at the output path")
	}

	v, err := Download(context.Background(), store, "k.tar.gz", out, artifactFor(data))
	if err != nil {
		t.Fatal(err)
	}
	if store.offsets[1] != 300 {
		t.Errorf("resume offset = %d, want 300", store.offsets[1])
	}
	if !v.Verified() {
		t.Errorf("verification = %+v", v)
	}
	got, _ := os.ReadFile(out)
	if !bytes.Equal(got, data) {
		t.Error("content mismatch")
	}
	if _, err := Download(context.Background(), store, "k.tar.gz", out, artifactFor(data)); err == nil {
		t.Error("must refuse to overwrite an existing file")
	}
}

func TestDownloadRestartsWhenObjectChanged(t *testing.T) {
	data := []byte(strings.Repeat("x", 500))
	store := &memStore{data: data, etag: `"e1"`, failAfter: 100}
	out := filepath.Join(t.TempDir(), "b.tgz")
	_, _ = Download(context.Background(), store, "k.tar.gz", out, artifactFor(data))

	store.etag = `"e2"`
	if _, err := Download(context.Background(), store, "k.tar.gz", out, artifactFor(data)); err != nil {
		t.Fatal(err)
	}
	if store.offsets[1] != 0 {
		t.Errorf("changed object must restart from 0, got %d", store.offsets[1])
	}
}

func TestDownloadChecksumMismatch(t *testing.T) {
	data := []byte("real bytes")
	store := &memStore{data: data, etag: `"e"`}
	art := artifactFor([]byte("other byte"))
	art.SizeBytes = int64(len(data))
	out := filepath.Join(t.TempDir(), "b.tgz")
	v, err := Download(context.Background(), store, "k.tar.gz", out, art)
	if err == nil || v.Verified() {
		t.Fatal("checksum mismatch must fail verification")
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("mismatched file must not be published")
	}
}

func TestDownloadWithoutReportedChecksumIsUnverified(t *testing.T) {
	data := []byte("bytes")
	store := &memStore{data: data, etag: `"e"`}
	v, err := Download(context.Background(), store, "k.tar.gz", filepath.Join(t.TempDir(), "b"), &Artifact{Location: "s3://b/k.tar.gz"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Verified() {
		t.Error("without a reported size/sha256 the download must not count as verified")
	}
}

func TestObjectKey(t *testing.T) {
	if _, err := ObjectKey(&Artifact{Location: "s3://other/k.tar.gz"}, "bucket"); err == nil {
		t.Error("bucket mismatch must be refused")
	}
	if _, err := ObjectKey(&Artifact{Location: "gs://bucket/k.tar.gz"}, "bucket"); !errors.Is(err, ErrUnsupportedBackend) {
		t.Error("gcs must be unsupported")
	}
	if _, err := ObjectKey(&Artifact{Location: "s3://bucket/wandb/data.db"}, "bucket"); err == nil {
		t.Error("non-bundle object must be refused")
	}
	key, err := ObjectKey(&Artifact{Location: "s3://bucket/a/b.tar.gz"}, "bucket")
	if err != nil || key != "a/b.tar.gz" {
		t.Errorf("key = %q %v", key, err)
	}
}
