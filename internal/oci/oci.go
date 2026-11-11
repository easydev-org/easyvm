// Package oci pulls a published disk from a container registry.
//
// A DISK IS AN OCI ARTIFACT, and the workflow that produced it pushed it with `oras`. Pulling one
// back is three HTTP requests and a copy, and doing it here rather than shelling out to `oras`
// buys three things the shell-out could not have:
//
//	RESUME   A 14 GiB transfer through a container proxy gets reset. `oras pull` starts again from
//	         byte zero; this asks for the rest and appends. That is why the Python implementation
//	         replaced `oras pull` with a hand-written client, and the reason is worth keeping.
//	ONE TAG  It does not fall back to another tag or registry. The caller names one artifact, and
//	         "the pull failed" is a fact rather than the end of a search.
//	NO TOOL  The image does not need `oras`, `crane` or `skopeo`, and a stage cannot fail because
//	         one of them is a version that changed its flags.
package oci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultRegistry is where this repository publishes.
const DefaultRegistry = "ghcr.io"

// Artifact is one published disk.
type Artifact struct {
	// Registry is the host, ghcr.io by default.
	Registry string
	// Repository is the image name, for example easydev-org/easydiskbake.
	Repository string
	// Tag is what the workflow published under.
	Tag string
	// Token is a registry credential. Empty means an anonymous pull, which works for a public
	// package and fails with a 401 for a private one - and that difference is reported as itself.
	Token string
}

// rel is the manifest reference for this artifact.
func (a Artifact) rel() string {
	reg := a.Registry
	if reg == "" {
		reg = DefaultRegistry
	}
	return fmt.Sprintf("%s/%s:%s", reg, a.Repository, a.Tag)
}

// manifest is the part of an OCI image manifest this package reads.
//
// AN ARTIFACT CARRIES ONE LAYER, THE DISK. `oras push file:disk.qcow2` writes a manifest with a
// single layer whose digest is the file's; reading the blob is the pull. The config is a small
// JSON document the publisher wrote and is not needed here.
type manifest struct {
	MediaType string `json:"mediaType"`
	Layers    []struct {
		MediaType   string            `json:"mediaType"`
		Digest      string            `json:"digest"`
		Size        int64             `json:"size"`
		Annotations map[string]string `json:"annotations,omitempty"`
	} `json:"layers"`
}

// Pull fetches the artifact into dest, resuming a partial file.
//
// THE PARTIAL FILE IS THE RESUME. A previous attempt leaves `dest.part` and this continues from its
// size; the file is renamed only when the byte count matches the manifest's. That is the same rule
// `lib/compress.py` applies to its output - a truncated file with the FINAL name is one a publish
// step would push.
func Pull(ctx context.Context, a Artifact, dest string, log func(string)) error {
	reg := a.Registry
	if reg == "" {
		reg = DefaultRegistry
	}
	// A REGISTRY THAT ANSWERS UNAUTHENTICATED IS THE NORMAL CASE for a public package, so the
	// token is acquired lazily: asking for one first costs a round trip on every pull.
	auth, err := acquireToken(ctx, reg, a.Repository, a.Token)
	if err != nil {
		return err
	}

	m, err := fetchManifest(ctx, reg, a.Repository, a.Tag, auth)
	if err != nil {
		return err
	}
	if len(m.Layers) == 0 {
		return fmt.Errorf("%s has no layers; this is not a disk artifact", a.rel())
	}
	// ONE LAYER IS THE DISK. More than one would mean the publisher changed the shape, and
	// guessing which to download is worse than saying so.
	if len(m.Layers) > 1 {
		return fmt.Errorf("%s has %d layers; this build expects one - the disk. Publishing changed "+
			"shape and this is where it should say so rather than pick", a.rel(), len(m.Layers))
	}
	layer := m.Layers[0]
	if layer.Digest == "" {
		return fmt.Errorf("%s: the layer has no digest", a.rel())
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	part := dest + ".part"

	// RESUME, AND SAY WHETHER IT IS ONE. A pull that silently continued from 8 GiB and one that
	// started over look identical in a log that does not say which.
	var offset int64
	if fi, err := os.Stat(part); err == nil {
		offset = fi.Size()
		if offset > layer.Size {
			log(fmt.Sprintf("  %s is larger than the artifact (%d > %d); starting over",
				filepath.Base(part), offset, layer.Size))
			offset = 0
		} else if offset > 0 {
			log(fmt.Sprintf("  resuming at %d of %d MiB", offset>>20, layer.Size>>20))
		}
	}

	if offset == 0 {
		_ = os.Remove(part)
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}

	blobURL := fmt.Sprintf("https://%s/v2/%s/blobs/%s", reg, a.Repository, layer.Digest)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, blobURL, nil)
	if err != nil {
		return err
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	applyAuth(req, auth)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("blob %s: %w", layer.Digest, err)
	}
	defer resp.Body.Close()

	// A SERVER THAT IGNORES Range ANSWERS 200 WITH THE WHOLE BLOB. Writing that from `offset`
	// would corrupt the file, so the two cases are told apart rather than hoped about.
	switch resp.StatusCode {
	case http.StatusPartialContent:
		// exactly what was asked for
	case http.StatusOK:
		if offset > 0 {
			log("  the registry did not honour the range; starting over")
			if err := f.Truncate(0); err != nil {
				return err
			}
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return err
			}
		}
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("the registry refused the pull (%d). This package is either private or "+
			"the token has expired; a public artifact needs none", resp.StatusCode)
	case http.StatusNotFound:
		return fmt.Errorf("%s is not in the registry (404). Check the tag: a stage that publishes "+
			"with an empty publish_tag gets a timestamped one, and this is what asking for the "+
			"wrong one looks like", a.rel())
	default:
		return fmt.Errorf("blob %s: HTTP %d", layer.Digest, resp.StatusCode)
	}

	written, err := io.Copy(f, resp.Body)
	if err != nil {
		return fmt.Errorf("copying the blob: %w (the file is left as %s.part and the next pull "+
			"resumes from there)", err, filepath.Base(dest))
	}
	if err := f.Sync(); err != nil {
		return err
	}
	final := offset
	if resp.StatusCode == http.StatusOK && offset > 0 {
		final = 0
	}
	final += written
	if final != layer.Size {
		return fmt.Errorf("%s: %d bytes of %d; the transfer was cut. The partial file is kept and "+
			"the next pull resumes from it", filepath.Base(dest), final, layer.Size)
	}
	if err := os.Rename(part, dest); err != nil {
		return err
	}
	log(fmt.Sprintf("  pulled %s -> %s (%d MiB)", a.rel(), dest, final>>20))
	return nil
}

// --- the three requests -----------------------------------------------------------------

type auth struct {
	token string
}

func applyAuth(req *http.Request, a auth) {
	if a.token == "" {
		return
	}
	// A BEARER TOKEN IS WHAT GHCR WANTS after the token exchange, and Basic is what it wants
	// before. The exchange below decides which, so this only ever sees one.
	req.Header.Set("Authorization", "Bearer "+a.token)
}

// acquireToken does the registry's token dance when it issues one.
//
// A REGISTRY WITHOUT A /token ENDPOINT IS NOT NECESSARILY ONE THAT REFUSES - an anonymous pull of a
// public package is answered directly. So a failure here is not fatal: the pull is attempted and a
// 401 from the blob is reported as a private package.
func acquireToken(ctx context.Context, reg, repository, credential string) (auth, error) {
	u := fmt.Sprintf("https://%s/token?scope=repository:%s:pull&service=%s",
		reg, url.QueryEscape(repository), reg)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return auth{}, err
	}
	if credential != "" {
		// ghcr accepts a PAT as the password with any username.
		req.SetBasicAuth("x", credential)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return auth{}, nil // anonymous attempt; the blob will say why if it fails
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return auth{}, nil
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return auth{}, nil
	}
	if body.Token != "" {
		return auth{token: body.Token}, nil
	}
	return auth{token: body.AccessToken}, nil
}

func fetchManifest(ctx context.Context, reg, repository, tag string, a auth) (manifest, error) {
	u := fmt.Sprintf("https://%s/v2/%s/manifests/%s", reg, repository, tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return manifest{}, err
	}
	// THE MANIFEST HAS TO BE ASKED FOR AS A MANIFEST. Without this header a registry may answer
	// an index it thinks is more appropriate, and an index has no layers.
	req.Header.Set("Accept",
		"application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
	applyAuth(req, a)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return manifest{}, fmt.Errorf("manifest %s/%s:%s: %w", reg, repository, tag, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return manifest{}, fmt.Errorf("%s/%s:%s is not in the registry (404)", reg, repository, tag)
	case http.StatusUnauthorized, http.StatusForbidden:
		return manifest{}, fmt.Errorf("%s/%s:%s needs credentials (%d); a public artifact "+
			"needs none", reg, repository, tag, resp.StatusCode)
	default:
		return manifest{}, fmt.Errorf("manifest %s/%s:%s: HTTP %d", reg, repository, tag,
			resp.StatusCode)
	}
	var m manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return manifest{}, fmt.Errorf("manifest for %s is not JSON: %w", tag, err)
	}
	return m, nil
}

// TagFrom splits `registry/repository:tag` into its parts, for a caller that has one string.
func TagFrom(ref string) (Artifact, error) {
	reg := DefaultRegistry
	rest := ref
	if i := strings.IndexByte(ref, '/'); i > 0 && strings.Contains(ref[:i], ".") {
		reg = ref[:i]
		rest = ref[i+1:]
	}
	idx := strings.LastIndexByte(rest, ':')
	if idx < 0 {
		return Artifact{}, errors.New("a tag is required: `repository:tag`")
	}
	return Artifact{Registry: reg, Repository: rest[:idx], Tag: rest[idx+1:]}, nil
}

// WithTimeout is the context every pull uses, so a hung transfer cannot hold a job forever.
func WithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
