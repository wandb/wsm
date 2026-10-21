package supportbundle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"syscall"
)

var (
	// ErrDestinationExists means the output file was already there.
	ErrDestinationExists = errors.New("destination already exists")
	// ErrDestinationAppeared means another file took the output path mid-download.
	ErrDestinationAppeared = errors.New("destination appeared during download - nothing overwritten")
)

// linkFile is os.Link; tests swap it to exercise the copy fallback.
var linkFile = os.Link

// Download describes a finished retrieve.
type Download struct {
	Path   string
	Bytes  int64
	SHA256 string
	// Verified is true when SHA256 matched the collector's reported digest.
	Verified bool
}

// DefaultOutputName is the archive's file name from its location.
func DefaultOutputName(location string) string {
	u, err := url.Parse(location)
	if err != nil || path.Base(u.Path) == "/" || path.Base(u.Path) == "." || !filepath.IsLocal(path.Base(u.Path)) {
		return "support-bundle.tar.gz"
	}
	return path.Base(u.Path)
}

// Stat returns the stored size of a bundle.
func (s *Session) Stat(ctx context.Context, location string) (int64, error) {
	var stdout bytes.Buffer
	if err := s.BundleManager(ctx, &stdout, nil, "retrieve", "--location", location, "--stat"); err != nil {
		return 0, err
	}
	var stat struct {
		Size *int64 `json:"size"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &stat); err != nil || stat.Size == nil || *stat.Size < 0 {
		return 0, fmt.Errorf("unexpected bundle-manager retrieve --stat output: %q", bytes.TrimSpace(stdout.Bytes()))
	}
	return *stat.Size, nil
}

// Retrieve downloads a bundle to a temp file next to dest and publishes it
// only after the size (and sha256, when known) match. dest is never replaced.
// progress, if set, gets the bytes written so far and the expected total.
func (s *Session) Retrieve(ctx context.Context, location, dest, expectedSHA256 string, progress func(written, total int64)) (*Download, error) {
	if _, err := os.Lstat(dest); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrDestinationExists, dest)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("check %s: %w", dest, err)
	}
	size, err := s.Stat(ctx, location)
	if err != nil {
		return nil, err
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".partial-*")
	if err != nil {
		return nil, fmt.Errorf("create temporary file next to %s: %w", dest, err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	hash := sha256.New()
	counter := &countingWriter{total: size, progress: progress}
	if err := s.BundleManager(ctx, io.MultiWriter(tmp, hash, counter), nil, "retrieve", "--location", location); err != nil {
		return nil, fmt.Errorf("download %s: %w", location, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if counter.n != size {
		return nil, fmt.Errorf("download %s: received %d bytes, expected %d", location, counter.n, size)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if expectedSHA256 != "" && digest != expectedSHA256 {
		return nil, fmt.Errorf("download %s: sha256 %s does not match the collector's %s", location, digest, expectedSHA256)
	}
	if err := tmp.Sync(); err != nil {
		return nil, fmt.Errorf("sync %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if err := finalize(ctx, tmpPath, dest); err != nil {
		return nil, err
	}
	return &Download{Path: dest, Bytes: counter.n, SHA256: digest, Verified: expectedSHA256 != ""}, nil
}

// finalize links tmpPath to dest, falling back to an O_EXCL copy (not atomic).
func finalize(ctx context.Context, tmpPath, dest string) error {
	err := linkFile(tmpPath, dest)
	if err == nil {
		_ = os.Remove(tmpPath)
		return nil
	}
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%w: %s", ErrDestinationAppeared, dest)
	}
	if !linkUnsupported(err) {
		return fmt.Errorf("publish %s: %w", dest, err)
	}
	return copyExclusive(ctx, tmpPath, dest)
}

func linkUnsupported(err error) bool {
	return errors.Is(err, errors.ErrUnsupported) ||
		errors.Is(err, syscall.EPERM) ||
		errors.Is(err, syscall.ENOTSUP) ||
		errors.Is(err, syscall.EXDEV)
}

// copyExclusive copies into a new dest, removing it if the copy fails.
func copyExclusive(ctx context.Context, tmpPath, dest string) (err error) {
	src, err := os.Open(tmpPath)
	if err != nil {
		return fmt.Errorf("reopen %s: %w", tmpPath, err)
	}
	defer func() { _ = src.Close() }()

	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%w: %s", ErrDestinationAppeared, dest)
	}
	if err != nil {
		return fmt.Errorf("create %s: %w", dest, err)
	}
	defer func() {
		if err != nil {
			_ = out.Close()
			_ = os.Remove(dest)
		}
	}()
	if _, err = io.Copy(out, contextReader{ctx: ctx, r: src}); err != nil {
		return fmt.Errorf("copy to %s: %w", dest, err)
	}
	if err = out.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", dest, err)
	}
	if err = out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", dest, err)
	}
	return nil
}

type countingWriter struct {
	n, total int64
	progress func(written, total int64)
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	if c.progress != nil {
		c.progress(c.n, c.total)
	}
	return len(p), nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
