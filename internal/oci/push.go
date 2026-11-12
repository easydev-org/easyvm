package oci

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// --- publishing ------------------------------------------------------------------------
//
// `Pull` HAD NO COUNTERPART, AND THAT WAS A STRUCTURAL GAP RATHER THAN AN OMISSION.
//
// Every stage AFTER an install starts from a published disk: its workflow leg says
// `"pull": "linux-<distro>-<stage>"`, and `easyvm pull` fetches it. NOTHING EVER PUSHED. The install
// legs declared `"out": "linux-<distro>.qcow2"` and produced it, and then the file travelled as a
// 4-to-8 GiB GitHub artifact that nothing downstream read - which is why five legs sat uploading for
// an hour while their walkthroughs had already finished.
//
// THE PROTOCOL IS THE MIRROR OF `Pull`, WHICH IS WHY IT IS SHORT: one layer holding the disk, a config
// blob saying nothing, and a manifest naming both. OCI's distribution spec is what makes this a
// thirty-line function rather than a storage format.
//
// THE TAG IS OVERWRITTEN, DELIBERATELY. A stage's output is the newest run's output, and a registry
// that refused a second push would make a re-run impossible - which is the opposite of what a stage
// needs after a fix.

// Push publishes the file at path under ref, which is `<repository>:<tag>` or just `<tag>` inside the
// artifact's repository.
func Push(ctx context.Context, a Artifact, path string, log func(string)) error {
	reg := a.Registry
	if reg == "" {
		reg = DefaultRegistry
	}
	if a.Repository == "" {
		return fmt.Errorf("push needs a repository; got %q", a.rel())
	}
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("no disk to push at %s: %w", path, err)
	}
	if fi.Size() == 0 {
		return fmt.Errorf("%s is empty; a stage that produced nothing must not publish it", path)
	}

	// PUSH NEEDS `pull,push` SCOPE, and asking for it is the only difference from a pull's token.
	auth, err := acquireTokenScope(ctx, reg, a.Repository, a.Token, "pull,push")
	if err != nil {
		return err
	}
	base := fmt.Sprintf("https://%s/v2/%s", reg, a.Repository)

	// THE LAYER, UPLOADED IN ONE PUT. Chunked upload exists for files a client cannot hold open for
	// ten minutes; a runner can, and one PUT is one failure mode instead of two.
	digest, size, err := sha256File(path)
	if err != nil {
		return err
	}
	layerType := "application/vnd.oci.image.layer.v1.tar+gzip"
	if err := putBlob(ctx, base, auth, digest, size, path,
		map[string]string{"org.opencontainers.image.title": filepath.Base(path)}); err != nil {
		return fmt.Errorf("uploading the disk layer: %w", err)
	}
	log(fmt.Sprintf("  pushed layer %s (%d MiB)", digest[:19], size>>20))

	// THE CONFIG BLOB, WHICH IS EMPTY BY CONVENTION. OCI requires one; nothing reads it.
	configBody := []byte("{}")
	configDigest := "sha256:" + hexSum(configBody)
	if err := putBlobBytes(ctx, base, auth, configDigest, configBody); err != nil {
		return fmt.Errorf("uploading the config: %w", err)
	}

	// AND THE MANIFEST, WHICH IS WHAT MAKES THE TAG EXIST.
	m := map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"digest":    configDigest,
			"size":      len(configBody),
		},
		"layers": []any{map[string]any{
			"mediaType": layerType,
			"digest":    digest,
			"size":      size,
			"annotations": map[string]string{
				"org.opencontainers.image.title": filepath.Base(path),
			},
		}},
	}
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/manifests/%s", base, url.PathEscape(a.Tag))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	applyAuth(req, auth)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("manifest %s: HTTP %d: %s", a.Tag, resp.StatusCode, firstLine(string(msg)))
	}
	log(fmt.Sprintf("  published %s", a.rel()))
	return nil
}

// acquireTokenScope is acquireToken with a caller-chosen scope, because a push needs `pull,push`.
func acquireTokenScope(ctx context.Context, reg, repository, credential, scope string) (auth, error) {
	u := fmt.Sprintf("https://%s/token?scope=repository:%s:%s&service=%s",
		reg, url.QueryEscape(repository), scope, reg)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return auth{}, err
	}
	if credential != "" {
		req.SetBasicAuth("x", credential)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return auth{}, fmt.Errorf("asking for a %s token: %w", scope, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return auth{}, fmt.Errorf("the registry refused a %s token (HTTP %d): %s", scope,
			resp.StatusCode, firstLine(string(msg)))
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return auth{}, err
	}
	return auth{token: body.Token}, nil
}

// putBlob uploads a file as one layer, by digest.
func putBlob(ctx context.Context, base string, a auth, digest string, size int64, path string,
	annotations map[string]string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return put(ctx, base, a, digest, size, f)
}

func putBlobBytes(ctx context.Context, base string, a auth, digest string, body []byte) error {
	return put(ctx, base, a, digest, int64(len(body)), bytes.NewReader(body))
}

func put(ctx context.Context, base string, a auth, digest string, size int64, body io.Reader) error {
	// THE TWO-STEP UPLOAD: START, THEN PUT AT THE LOCATION IT GIVES. A registry that supports
	// monolithic upload answers the POST with a Location that accepts the whole body.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/blobs/uploads/", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Length", "0")
	applyAuth(req, a)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("starting the upload: HTTP %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return fmt.Errorf("the registry did not say where to upload")
	}
	if !strings.HasPrefix(loc, "http") {
		reg := req.URL.Scheme + "://" + req.URL.Host
		loc = reg + loc
	}
	sep := "?"
	if strings.Contains(loc, "?") {
		sep = "&"
	}
	u := loc + sep + "digest=" + url.QueryEscape(digest)
	up, err := http.NewRequestWithContext(ctx, http.MethodPut, u, body)
	if err != nil {
		return err
	}
	up.ContentLength = size
	up.Header.Set("Content-Type", "application/octet-stream")
	applyAuth(up, a)
	uresp, err := http.DefaultClient.Do(up)
	if err != nil {
		return err
	}
	defer uresp.Body.Close()
	if uresp.StatusCode != http.StatusCreated && uresp.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(io.LimitReader(uresp.Body, 2048))
		return fmt.Errorf("uploading the blob: HTTP %d: %s", uresp.StatusCode, firstLine(string(msg)))
	}
	return nil
}

func sha256File(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), n, nil
}

func hexSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// firstLine is the first line of a registry's error body, for a message that stays readable.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}
