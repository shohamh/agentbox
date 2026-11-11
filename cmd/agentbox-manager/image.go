package main

import (
	"archive/tar"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

var imgMu sync.Mutex

// ensureImage pulls ref@digest from the internal registry (the digest is
// pinned by the signed bundle, so the content is what policy allows), and
// unpacks it read-only into the image cache on the data disk.
func (m *Manager) ensureImage(ref, digest string) (string, error) {
	imgMu.Lock()
	defer imgMu.Unlock()
	if !strings.HasPrefix(digest, "sha256:") {
		return "", fmt.Errorf("image %s has no pinned digest in the bundle", ref)
	}
	dir := filepath.Join(m.cfg.DataDir, "images", strings.TrimPrefix(digest, "sha256:"))
	rootfs := filepath.Join(dir, "rootfs")
	if _, err := os.Stat(filepath.Join(dir, "complete")); err == nil {
		return rootfs, nil
	}
	start := time.Now()
	r, err := name.ParseReference(ref+"@"+digest, name.Insecure)
	if err != nil {
		return "", err
	}
	// The signed bundle pins the digest of what was pushed (often a
	// multi-platform index). Verify that descriptor, then resolve the
	// linux/amd64 image inside it; every layer is content-addressed below it.
	desc, err := remote.Get(r, remote.WithTransport(&http.Transport{Proxy: nil}))
	if err != nil {
		return "", fmt.Errorf("pull %s: %w", r, err)
	}
	if desc.Digest.String() != digest {
		return "", fmt.Errorf("pulled image digest %s does not match policy %s", desc.Digest, digest)
	}
	img, err := desc.Image()
	if err != nil {
		return "", fmt.Errorf("resolve image %s: %w", r, err)
	}
	os.RemoveAll(dir)
	tmp := rootfs + ".partial"
	os.MkdirAll(tmp, 0o755)
	rc := mutate.Extract(img)
	defer rc.Close()
	if err := untar(rc, tmp); err != nil {
		return "", fmt.Errorf("unpack: %w", err)
	}
	if err := os.Rename(tmp, rootfs); err != nil {
		return "", err
	}
	os.WriteFile(filepath.Join(dir, "complete"), []byte(time.Now().String()), 0o644)
	log.Printf("image %s@%s cached in %s", ref, digest[:19], time.Since(start).Round(time.Millisecond))
	m.emit("image.pulled", map[string]any{"image": ref, "image.digest": digest, "ms": time.Since(start).Milliseconds()})
	return rootfs, nil
}

func untar(r io.Reader, dst string) error {
	tr := tar.NewReader(r)
	type link struct{ name, target string }
	var hardlinks []link
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		clean := filepath.Clean("/" + h.Name)
		if strings.Contains(h.Name, "..") && clean != filepath.Clean("/"+strings.ReplaceAll(h.Name, "..", "")) {
			return fmt.Errorf("unsafe path %q", h.Name)
		}
		p := filepath.Join(dst, clean)
		mode := os.FileMode(h.Mode) & os.ModePerm
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			os.Chmod(p, mode|os.FileMode(h.Mode)&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky))
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.Remove(p)
			f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
			os.Chmod(p, tarMode(h))
		case tar.TypeSymlink:
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.Remove(p)
			if err := os.Symlink(h.Linkname, p); err != nil {
				return err
			}
		case tar.TypeLink:
			hardlinks = append(hardlinks, link{p, filepath.Join(dst, filepath.Clean("/"+h.Linkname))})
			continue
		default:
			continue // devices and fifos are not needed in sandbox images
		}
		os.Lchown(p, h.Uid, h.Gid)
		if h.Typeflag != tar.TypeSymlink {
			os.Chtimes(p, h.ModTime, h.ModTime)
		}
	}
	for _, l := range hardlinks {
		os.Remove(l.name)
		if err := os.Link(l.target, l.name); err != nil {
			return err
		}
	}
	return nil
}

func tarMode(h *tar.Header) os.FileMode {
	m := os.FileMode(h.Mode) & os.ModePerm
	if h.Mode&04000 != 0 {
		m |= os.ModeSetuid
	}
	if h.Mode&02000 != 0 {
		m |= os.ModeSetgid
	}
	if h.Mode&01000 != 0 {
		m |= os.ModeSticky
	}
	return m
}
