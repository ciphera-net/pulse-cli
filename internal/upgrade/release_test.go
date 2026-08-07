package upgrade

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// realAssets is the asset list published for v1.0.0, verbatim. Naming it here
// means a change to .goreleaser.yaml's name_template that this code cannot pick
// an asset from shows up as a failing test rather than as "no build for
// darwin/arm64" on a user's machine.
func realAssets() *Release {
	rel := &Release{
		Tag: "v1.0.0",
		URL: "https://github.com/ciphera-net/pulse-cli/releases/tag/v1.0.0",
		Assets: []Asset{
			{Name: "checksums.txt", Size: 582},
			{Name: "checksums.txt.sig", Size: 96},
		},
	}
	for _, name := range []string{
		"pulse_1.0.0_darwin_amd64.tar.gz",
		"pulse_1.0.0_darwin_arm64.tar.gz",
		"pulse_1.0.0_linux_amd64.tar.gz",
		"pulse_1.0.0_linux_arm64.tar.gz",
		"pulse_1.0.0_windows_amd64.zip",
		"pulse_1.0.0_windows_arm64.zip",
	} {
		rel.Assets = append(rel.Assets,
			Asset{Name: name, Size: 3_000_000, URL: "https://example.invalid/" + name},
			Asset{Name: name + ".sig", Size: 96, URL: "https://example.invalid/" + name + ".sig"})
	}
	return rel
}

func TestPickAssetsFindsEveryPublishedPlatform(t *testing.T) {
	rel := realAssets()
	cases := map[string][2]string{
		"darwin/arm64":  {"darwin", "arm64"},
		"darwin/amd64":  {"darwin", "amd64"},
		"linux/amd64":   {"linux", "amd64"},
		"linux/arm64":   {"linux", "arm64"},
		"windows/amd64": {"windows", "amd64"},
		"windows/arm64": {"windows", "arm64"},
	}
	for name, target := range cases {
		t.Run(name, func(t *testing.T) {
			archive, sig, err := PickAssets(rel, target[0], target[1])
			if err != nil {
				t.Fatalf("PickAssets: %v", err)
			}
			if !strings.Contains(archive.Name, "_"+target[0]+"_"+target[1]+".") {
				t.Errorf("picked %q for %s", archive.Name, name)
			}
			// * checksums.txt is also signed and also matches nothing about the
			// * platform. Picking it would produce a "binary" that is a text file.
			if strings.HasPrefix(archive.Name, "checksums") {
				t.Errorf("picked the checksums file as the archive: %q", archive.Name)
			}
			if sig.Name != archive.Name+".sig" {
				t.Errorf("signature = %q, want %q", sig.Name, archive.Name+".sig")
			}
		})
	}
}

func TestPickAssetsRefusesAPlatformTheReleaseDoesNotBuild(t *testing.T) {
	if _, _, err := PickAssets(realAssets(), "freebsd", "riscv64"); err == nil {
		t.Fatal("PickAssets invented a build for freebsd/riscv64")
	}
}

// * An archive published without its signature must be refused, not installed
// * with a warning. "The signature was missing so we installed it anyway" is one
// * branch that removes the entire protection, and it is exactly the shape an
// * attacker who can publish assets would produce.
func TestPickAssetsRefusesAnArchiveWithNoSignature(t *testing.T) {
	rel := &Release{
		Tag: "v1.1.0",
		Assets: []Asset{
			{Name: "pulse_1.1.0_linux_amd64.tar.gz", URL: "https://example.invalid/a"},
		},
	}
	_, _, err := PickAssets(rel, "linux", "amd64")
	if err == nil {
		t.Fatal("an unsigned archive was accepted")
	}
	if !strings.Contains(err.Error(), ".sig") {
		t.Errorf("the refusal does not say what was missing: %v", err)
	}
}

func TestLatestReadsTheReleaseFeed(t *testing.T) {
	var gotUA, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA, gotAccept = r.Header.Get("User-Agent"), r.Header.Get("Accept")
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("the upgrade check sent an Authorization header: %q", auth)
		}
		w.Write([]byte(`{"tag_name":"v1.1.0","html_url":"https://example.invalid/v1.1.0",
			"assets":[{"name":"pulse_1.1.0_linux_amd64.tar.gz","browser_download_url":"https://example.invalid/a","size":42}]}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClient("pulse-cli/v1.0.0")
	c.ReleasesURL = srv.URL

	rel, err := c.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if rel.Tag != "v1.1.0" {
		t.Errorf("tag = %q, want v1.1.0", rel.Tag)
	}
	if len(rel.Assets) != 1 || rel.Assets[0].Size != 42 {
		t.Errorf("assets = %+v", rel.Assets)
	}
	// * GitHub answers 403 to a request with no User-Agent, which would make
	// * every check fail with a message about rate limits.
	if gotUA != "pulse-cli/v1.0.0" {
		t.Errorf("User-Agent = %q", gotUA)
	}
	if gotAccept != "application/vnd.github+json" {
		t.Errorf("Accept = %q", gotAccept)
	}
}

func TestLatestReportsARefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	c := NewClient("pulse-cli/test")
	c.ReleasesURL = srv.URL

	_, err := c.Latest(context.Background())
	if err == nil {
		t.Fatal("a 403 was reported as a successful check")
	}
	// * GitHub's unauthenticated limit is per IP, so a shared CI runner hits it
	// * for reasons that have nothing to do with this user.
	if !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("a 403 does not explain the likely cause: %v", err)
	}
}

func TestLatestRefusesAFeedWithNoVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"assets":[]}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClient("pulse-cli/test")
	c.ReleasesURL = srv.URL

	if _, err := c.Latest(context.Background()); err == nil {
		t.Fatal(`a release with no tag_name was accepted; the CLI would compare against ""`)
	}
}
