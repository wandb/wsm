package supportbundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// Verification records what a download checked against the reported result.
type Verification struct {
	Size           int64
	SHA256         string
	SizeVerified   bool
	SHA256Verified bool
}

// Verified reports whether both size and checksum matched the result, the
// precondition for deleting the bucket copy.
func (v Verification) Verified() bool { return v.SizeVerified && v.SHA256Verified }

type partialState struct {
	Key  string `json:"key"`
	ETag string `json:"etag"`
	Size int64  `json:"size"`
}

// Download fetches key into output, resuming a previous partial download of
// the same object. The file only appears at output once it is complete and
// (when the result reported them) its size and sha256 match.
func Download(ctx context.Context, store ObjectStore, key, output string, art *Artifact) (Verification, error) {
	if _, err := os.Stat(output); err == nil {
		return Verification{}, fmt.Errorf("%s already exists; refusing to overwrite", output)
	}
	info, err := store.Stat(ctx, key)
	if err != nil {
		return Verification{}, err
	}
	if art.SizeBytes > 0 && info.Size != art.SizeBytes {
		return Verification{}, fmt.Errorf("object size %d does not match reported size %d", info.Size, art.SizeBytes)
	}

	partial, statePath := output+".partial", output+".partial.json"
	offset := resumeOffset(partial, statePath, key, info)
	if err := writeState(statePath, partialState{Key: key, ETag: info.ETag, Size: info.Size}); err != nil {
		return Verification{}, err
	}

	if offset < info.Size {
		flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
		if offset == 0 {
			flags |= os.O_TRUNC
		}
		f, err := os.OpenFile(partial, flags, 0o600)
		if err != nil {
			return Verification{}, err
		}
		body, err := store.ReadFrom(ctx, key, offset, info.ETag)
		if err != nil {
			_ = f.Close()
			return Verification{}, err
		}
		_, copyErr := io.Copy(f, body)
		_ = body.Close()
		if err := f.Close(); err != nil && copyErr == nil {
			copyErr = err
		}
		if copyErr != nil {
			return Verification{}, fmt.Errorf("download interrupted (re-run to resume): %w", copyErr)
		}
	}

	v, err := verify(partial, info.Size, art)
	if err != nil {
		return v, err
	}
	if err := os.Rename(partial, output); err != nil {
		return v, err
	}
	_ = os.Remove(statePath)
	return v, nil
}

func resumeOffset(partial, statePath, key string, info ObjectInfo) int64 {
	raw, err := os.ReadFile(statePath)
	if err != nil {
		return 0
	}
	var st partialState
	if json.Unmarshal(raw, &st) != nil || st.Key != key || st.ETag != info.ETag || st.Size != info.Size {
		return 0
	}
	fi, err := os.Stat(partial)
	if err != nil || fi.Size() > info.Size {
		return 0
	}
	return fi.Size()
}

func writeState(path string, st partialState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func verify(path string, expectedSize int64, art *Artifact) (Verification, error) {
	f, err := os.Open(path)
	if err != nil {
		return Verification{}, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return Verification{}, err
	}
	v := Verification{Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}
	if n != expectedSize {
		return v, fmt.Errorf("downloaded %d bytes but the object is %d bytes", n, expectedSize)
	}
	v.SizeVerified = art.SizeBytes > 0 && n == art.SizeBytes
	if art.SHA256 != "" {
		if v.SHA256 != art.SHA256 {
			_ = os.Remove(path)
			return v, errors.New("sha256 mismatch: the downloaded bundle does not match the checksum Lumen reported; partial file removed")
		}
		v.SHA256Verified = true
	}
	return v, nil
}
