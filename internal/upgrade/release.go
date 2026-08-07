package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultReleasesURL is the release feed.
//
// GitHub's unauthenticated API, on purpose. Checking for an update must not
// spend the user's Pulse quota, and the Pulse API key must never be sent
// anywhere but pulse-api.ciphera.net — so this path builds its own HTTP request
// from nothing and never sees a credential.
const DefaultReleasesURL = "https://api.github.com/repos/ciphera-net/pulse-cli/releases/latest"

// maxAssetBytes caps a download. The published archives are ~3 MB; the cap is
// two orders of magnitude above that and exists so a hostile or broken endpoint
// cannot make the CLI read until memory runs out.
const maxAssetBytes = 256 << 20

// maxAPIBytes caps the release JSON, which is a few kilobytes.
const maxAPIBytes = 4 << 20

// Release is the subset of GitHub's release object this command needs.
type Release struct {
	Tag        string  `json:"tag_name"`
	URL        string  `json:"html_url"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

// Asset is one published file.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// Client reads releases and downloads their assets.
type Client struct {
	ReleasesURL string
	UserAgent   string
	HTTP        *http.Client
}

// NewClient builds a client for the real feed.
func NewClient(userAgent string) *Client {
	return &Client{
		ReleasesURL: DefaultReleasesURL,
		UserAgent:   userAgent,
		// * Generous, because this timeout has to cover a 3 MB download on a
		// * slow link. The metadata call gets its own much shorter deadline in
		// * Latest — a hung API call must not look like a hung terminal.
		HTTP: &http.Client{Timeout: 10 * time.Minute},
	}
}

// Latest returns the newest published release.
//
// /releases/latest already excludes drafts and pre-releases, so a tagged rc
// never becomes an upgrade offer.
func (c *Client) Latest(ctx context.Context) (*Release, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	body, err := c.get(ctx, c.ReleasesURL, "application/vnd.github+json", maxAPIBytes)
	if err != nil {
		return nil, err
	}

	var rel Release
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, fmt.Errorf("could not read the release list: %w", err)
	}
	if rel.Tag == "" {
		return nil, fmt.Errorf("the release list names no version")
	}
	return &rel, nil
}

// Download fetches one asset in full.
func (c *Client) Download(ctx context.Context, a Asset) ([]byte, error) {
	return c.get(ctx, a.URL, "application/octet-stream", maxAssetBytes)
}

func (c *Client) get(ctx context.Context, url, accept string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	// * GitHub answers 403 to a request with no User-Agent.
	req.Header.Set("User-Agent", c.UserAgent)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			return nil, fmt.Errorf("GitHub refused the request (HTTP %d) — its unauthenticated "+
				"rate limit is per IP address, so a shared runner can exhaust it; try again later",
				resp.StatusCode)
		}
		return nil, fmt.Errorf("GitHub returned HTTP %d for %s", resp.StatusCode, url)
	}

	// * limit+1 so a body that hits the cap is detected rather than silently
	// * truncated into a "corrupt archive" further down.
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", url, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes and was not read", url, limit)
	}
	return body, nil
}

// PickAssets finds the archive for one platform and the signature that covers
// it.
//
// Matched by the "_goos_goarch." infix rather than by rebuilding goreleaser's
// name_template here. The template lives in .goreleaser.yaml and can change —
// the version segment alone already differs between "1.0.0" and the "v1.0.0"
// tag — and a second copy of it in Go would fail as "no build for darwin/arm64"
// on the release that changed it.
func PickAssets(r *Release, goos, goarch string) (archive, signature Asset, err error) {
	infix := "_" + goos + "_" + goarch + "."
	for _, a := range r.Assets {
		if !strings.Contains(a.Name, infix) || !isArchive(a.Name) {
			continue
		}
		archive = a
		break
	}
	if archive.Name == "" {
		return Asset{}, Asset{}, fmt.Errorf("release %s publishes no build for %s/%s", r.Tag, goos, goarch)
	}

	want := archive.Name + ".sig"
	for _, a := range r.Assets {
		if a.Name == want {
			signature = a
			break
		}
	}
	if signature.URL == "" {
		// * Refused, not downgraded to a warning. An unsigned archive is exactly
		// * what an attacker who can publish assets would produce, and "the
		// * signature was missing so we installed it anyway" removes the whole
		// * protection with one branch.
		return Asset{}, Asset{}, fmt.Errorf("release %s publishes %s without %s — refusing to install an "+
			"archive nothing vouches for", r.Tag, archive.Name, want)
	}
	return archive, signature, nil
}

func isArchive(name string) bool {
	return strings.HasSuffix(name, ".tar.gz") ||
		strings.HasSuffix(name, ".tgz") ||
		strings.HasSuffix(name, ".zip")
}
