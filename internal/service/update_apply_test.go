package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/smalex-z/gopher/internal/build"
	"github.com/smalex-z/gopher/internal/db"
)

// fakeRelease mirrors the subset of the GitHub release JSON update.go decodes.
type fakeAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}
type fakeRelease struct {
	TagName    string      `json:"tag_name"`
	Prerelease bool        `json:"prerelease"`
	Draft      bool        `json:"draft"`
	Assets     []fakeAsset `json:"assets"`
}

// releaseAssetName is what release.yml actually publishes for this arch —
// dist/gopher-linux-<arch> hashed as "dist/gopher-linux-<arch>" in
// SHA256SUMS.txt. The tests pin that naming contract.
func releaseAssetName() string {
	return "gopher-linux-" + runtime.GOARCH
}

// startFakeGitHub serves the two API shapes update.go hits plus asset
// downloads. binary is the fake release binary; sumsLine the SHA256SUMS.txt
// body ("" = omit the sums asset entirely). An optional trailing argument is
// the SHA256SUMS.txt.minisig body ("" / absent = no signature asset).
func startFakeGitHub(t *testing.T, tag string, prerelease bool, binary []byte, sums string, minisig ...string) *httptest.Server {
	t.Helper()
	sig := ""
	if len(minisig) > 0 {
		sig = minisig[0]
	}
	mux := http.NewServeMux()
	var srv *httptest.Server
	release := func() fakeRelease {
		r := fakeRelease{TagName: tag, Prerelease: prerelease}
		r.Assets = append(r.Assets, fakeAsset{Name: releaseAssetName(), URL: srv.URL + "/dl/" + releaseAssetName()})
		if sums != "" {
			r.Assets = append(r.Assets, fakeAsset{Name: "SHA256SUMS.txt", URL: srv.URL + "/dl/SHA256SUMS.txt"})
		}
		if sig != "" {
			r.Assets = append(r.Assets, fakeAsset{Name: "SHA256SUMS.txt.minisig", URL: srv.URL + "/dl/SHA256SUMS.txt.minisig"})
		}
		return r
	}
	mux.HandleFunc("/repos/smalex-z/gopher/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, release())
	})
	mux.HandleFunc("/repos/smalex-z/gopher/releases", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []fakeRelease{release()})
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".minisig"):
			fmt.Fprint(w, sig)
		case strings.HasSuffix(r.URL.Path, "SHA256SUMS.txt"):
			fmt.Fprint(w, sums)
		default:
			_, _ = w.Write(binary)
		}
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode fake release: %v", err)
	}
}

// pointUpdatesAt redirects the GitHub base URL and the running version for
// one test, restoring both after.
func pointUpdatesAt(t *testing.T, srv *httptest.Server, version string) {
	t.Helper()
	origURL, origVer := githubAPIBaseURL, build.Version
	githubAPIBaseURL, build.Version = srv.URL, version
	t.Cleanup(func() { githubAPIBaseURL, build.Version = origURL, origVer })
}

func setChannel(t *testing.T, channel string) {
	t.Helper()
	if err := db.MutateSettings(func(s *db.AppSettings) error {
		s.UpdateChannel = channel
		return nil
	}); err != nil {
		t.Fatalf("set channel: %v", err)
	}
}

func TestUpdateCheck_StableChannel(t *testing.T) {
	initTestDB(t)
	srv := startFakeGitHub(t, "v0.2.0", false, []byte("bin"), "irrelevant")
	pointUpdatesAt(t, srv, "v0.1.0")
	setChannel(t, "stable")

	info, err := NewUpdateService().Check()
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if info.LatestVersion != "v0.2.0" || !info.UpdateAvailable {
		t.Errorf("Check = %+v, want latest v0.2.0 available", info)
	}
}

// Regression: a repo with only prereleases makes GitHub's /releases/latest
// (stable-only) return 404. Check must report that in-band, not error — a 500
// here unmounted the dashboard's version card, channel picker included, so
// switching to stable was a one-way door until the first stable release.
func TestUpdateCheck_StableChannelWithNoStableRelease(t *testing.T) {
	initTestDB(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/smalex-z/gopher/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	pointUpdatesAt(t, srv, "v0.1.0-beta.20")
	setChannel(t, "stable")

	info, err := NewUpdateService().Check()
	if err != nil {
		t.Fatalf("Check must degrade gracefully, got error: %v", err)
	}
	if info.UpdateAvailable {
		t.Errorf("no stable release must mean no update available, got %+v", info)
	}
	if !strings.Contains(info.CheckError, "no stable release") {
		t.Errorf("CheckError = %q, want a 'no stable release' note", info.CheckError)
	}
	if info.Channel != "stable" || info.CurrentVersion != "v0.1.0-beta.20" {
		t.Errorf("channel/current must survive the failed lookup, got %+v", info)
	}
}

func TestUpdateCheck_BetaChannelViaList(t *testing.T) {
	initTestDB(t)
	srv := startFakeGitHub(t, "v0.1.0-beta.19", true, []byte("bin"), "irrelevant")
	pointUpdatesAt(t, srv, "v0.1.0-beta.18")
	setChannel(t, "beta")

	info, err := NewUpdateService().Check()
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if info.LatestVersion != "v0.1.0-beta.19" || !info.UpdateAvailable {
		t.Errorf("Check = %+v, want beta.19 available", info)
	}
}

// The exact asset names release.yml publishes must stay resolvable —
// renaming an asset in the release pipeline should fail HERE, not in
// production dashboards that silently stop updating.
func TestReleaseAssetNamingContract(t *testing.T) {
	var r githubRelease
	blob := `{
		"tag_name": "v0.1.0",
		"assets": [
			{"name": "gopher-linux-amd64", "browser_download_url": "https://x/gopher-linux-amd64"},
			{"name": "gopher-linux-arm64", "browser_download_url": "https://x/gopher-linux-arm64"},
			{"name": "SHA256SUMS.txt", "browser_download_url": "https://x/SHA256SUMS.txt"}
		]
	}`
	if err := json.Unmarshal([]byte(blob), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got := findAssetURL(&r); got != "https://x/"+releaseAssetName() {
		t.Errorf("findAssetURL = %q, want the %s asset", got, releaseAssetName())
	}
	if name, got := findChecksumsAsset(&r); got != "https://x/SHA256SUMS.txt" || name != "SHA256SUMS.txt" {
		t.Errorf("findChecksumsAsset = (%q, %q), want the SHA256SUMS.txt asset", name, got)
	}
}

func TestUpdateApply_RefusesWithoutChecksums(t *testing.T) {
	initTestDB(t)
	srv := startFakeGitHub(t, "v0.2.0", false, []byte("bin"), "")
	pointUpdatesAt(t, srv, "v0.1.0")
	setChannel(t, "stable")

	origInstall := installVerifiedBinary
	installVerifiedBinary = func(string) error {
		t.Error("installVerifiedBinary must not run without a SHA256SUMS asset")
		return nil
	}
	t.Cleanup(func() { installVerifiedBinary = origInstall })

	err := NewUpdateService().Apply()
	if err == nil || !strings.Contains(err.Error(), "SHA256SUMS") {
		t.Fatalf("Apply = %v, want refusal naming SHA256SUMS", err)
	}
}

// Apply must enforce the same forward-only rule Check advertises by — it's
// reachable directly via the API, so without its own gate a channel whose
// latest is older than the running build installs as a silent downgrade.
func TestUpdateApply_RefusesDowngrade(t *testing.T) {
	initTestDB(t)
	srv := startFakeGitHub(t, "v0.1.0", false, []byte("bin"), "irrelevant")
	pointUpdatesAt(t, srv, "v0.2.0") // running build is newer than channel's latest
	setChannel(t, "stable")

	origInstall := installVerifiedBinary
	installVerifiedBinary = func(string) error {
		t.Error("installVerifiedBinary must not run for a downgrade")
		return nil
	}
	t.Cleanup(func() { installVerifiedBinary = origInstall })

	err := NewUpdateService().Apply()
	if err == nil || !strings.Contains(err.Error(), "refusing to downgrade") {
		t.Fatalf("Apply = %v, want downgrade refusal", err)
	}
}

func TestUpdateApply_RefusesSameVersionReinstall(t *testing.T) {
	initTestDB(t)
	srv := startFakeGitHub(t, "v0.2.0", false, []byte("bin"), "irrelevant")
	pointUpdatesAt(t, srv, "v0.2.0") // already running the channel's latest
	setChannel(t, "stable")

	origInstall := installVerifiedBinary
	installVerifiedBinary = func(string) error {
		t.Error("installVerifiedBinary must not run for a same-version reinstall")
		return nil
	}
	t.Cleanup(func() { installVerifiedBinary = origInstall })

	if err := NewUpdateService().Apply(); err == nil {
		t.Fatal("Apply must refuse a same-version reinstall")
	}
}

func TestUpdateApply_ChecksumMismatchAborts(t *testing.T) {
	initTestDB(t)
	sums := fmt.Sprintf("%064d  dist/%s\n", 0, releaseAssetName())
	srv := startFakeGitHub(t, "v0.2.0", false, []byte("real binary bytes"), sums)
	pointUpdatesAt(t, srv, "v0.1.0")
	setChannel(t, "stable")

	origInstall := installVerifiedBinary
	installVerifiedBinary = func(tmpPath string) error {
		t.Error("installVerifiedBinary must not run on checksum mismatch")
		os.Remove(tmpPath)
		return nil
	}
	t.Cleanup(func() { installVerifiedBinary = origInstall })

	err := NewUpdateService().Apply()
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Apply = %v, want checksum-mismatch refusal", err)
	}
}

// downloadSmall must reject a response that exceeds its cap rather than
// silently truncating it — a truncated sums file misdiagnoses later as a
// signature failure or missing checksum entry.
func TestDownloadSmall_RefusesOversizedResponse(t *testing.T) {
	body := strings.Repeat("a", 100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)

	if _, err := downloadSmall(srv.Client(), srv.URL, 99); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("downloadSmall over cap = %v, want size-limit error", err)
	}
	if data, err := downloadSmall(srv.Client(), srv.URL, 100); err != nil || len(data) != 100 {
		t.Fatalf("downloadSmall at exactly the cap = (%d bytes, %v), want the full body", len(data), err)
	}
}

func TestUpdateApply_VerifiedDownloadReachesInstall(t *testing.T) {
	initTestDB(t)
	binary := []byte("the new gopher binary")
	sum := sha256.Sum256(binary)
	// Same two-column, dist/-prefixed layout release.yml's sha256sum step emits.
	sums := hex.EncodeToString(sum[:]) + "  dist/" + releaseAssetName() + "\n"
	srv := startFakeGitHub(t, "v0.2.0", false, binary, sums)
	pointUpdatesAt(t, srv, "v0.1.0")
	setChannel(t, "stable")

	var installed string
	origInstall := installVerifiedBinary
	installVerifiedBinary = func(tmpPath string) error {
		got, err := os.ReadFile(tmpPath)
		if err != nil {
			t.Errorf("read verified tmp: %v", err)
		} else if string(got) != string(binary) {
			t.Errorf("verified tmp content mismatch (%d bytes)", len(got))
		}
		installed = tmpPath
		return os.Remove(tmpPath)
	}
	t.Cleanup(func() { installVerifiedBinary = origInstall })

	if err := NewUpdateService().Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if installed == "" {
		t.Fatal("installVerifiedBinary was never invoked for a verified download")
	}
}
