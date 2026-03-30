package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/smalex-z/gopher/internal/build"
)

const githubRepo = "smalex-z/gopher"

type UpdateService struct{}

type UpdateInfo struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	UpdateAvailable bool   `json:"update_available"`
}

type githubRelease struct {
	TagName    string `json:"tag_name"`
	Prerelease bool   `json:"prerelease"`
	Draft      bool   `json:"draft"`
	Assets     []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

type semverParts struct {
	major      int
	minor      int
	patch      int
	prerelease string
}

func NewUpdateService() *UpdateService {
	return &UpdateService{}
}

func (s *UpdateService) Check() (*UpdateInfo, error) {
	info := &UpdateInfo{
		CurrentVersion:  build.Version,
		LatestVersion:   build.Version,
		UpdateAvailable: false,
	}

	if build.Version == "dev" {
		return info, nil
	}

	release, err := fetchLatestRelease(build.Version)
	if err != nil {
		return nil, err
	}

	info.LatestVersion = release.TagName
	info.UpdateAvailable = isNewer(release.TagName, build.Version)
	return info, nil
}

func (s *UpdateService) Apply() error {
	release, err := fetchLatestRelease(build.Version)
	if err != nil {
		return err
	}

	downloadURL := findAssetURL(release)
	if downloadURL == "" {
		return fmt.Errorf("no compatible binary found for linux/%s in release %s", runtime.GOARCH, release.TagName)
	}

	// Download to a temp file
	tmpFile, err := os.CreateTemp("", "gopher-update-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	httpClient := &http.Client{Timeout: 5 * time.Minute}
	resp, err := httpClient.Get(downloadURL)
	if err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("failed to download update: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("failed to write update: %w", err)
	}
	tmpFile.Close()

	if err := os.Chmod(tmpPath, 0755); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to chmod update binary: %w", err)
	}

	// Find current binary path
	binaryPath, err := os.Executable()
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to find current binary path: %w", err)
	}

	// Replace binary (needs elevated privileges since binary lives in /opt/gopher/)
	sudo := privilegedCmdPrefix()
	mvArgs := append(append([]string{}, sudo...), "mv", tmpPath, binaryPath)
	if out, err := exec.Command(mvArgs[0], mvArgs[1:]...).CombinedOutput(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to replace binary: %w — %s", err, strings.TrimSpace(string(out)))
	}

	// Restart the service after a short delay so this HTTP response can be delivered
	go func() {
		time.Sleep(time.Second)
		restartArgs := append(append([]string{}, privilegedCmdPrefix()...), "systemctl", "restart", "gopher")
		_ = exec.Command(restartArgs[0], restartArgs[1:]...).Run()
	}()

	return nil
}

func fetchLatestRelease(currentVersion string) (*githubRelease, error) {
	if isPrereleaseTag(currentVersion) {
		return fetchLatestReleaseIncludingPrerelease()
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "gopher/"+build.Version)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to reach GitHub API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned HTTP %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("failed to parse release info: %w", err)
	}
	return &release, nil
}

func fetchLatestReleaseIncludingPrerelease() (*githubRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=30", githubRepo)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "gopher/"+build.Version)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to reach GitHub API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned HTTP %d", resp.StatusCode)
	}

	var releases []githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("failed to parse release info: %w", err)
	}

	var best *githubRelease
	for i := range releases {
		r := &releases[i]
		if r.Draft || strings.TrimSpace(r.TagName) == "" {
			continue
		}
		if best == nil || isNewer(r.TagName, best.TagName) {
			best = r
		}
	}

	if best == nil {
		return nil, fmt.Errorf("no releases found")
	}

	return best, nil
}

// findAssetURL looks for a Linux binary asset matching the current architecture.
func findAssetURL(release *githubRelease) string {
	var archKeywords []string
	switch runtime.GOARCH {
	case "amd64":
		archKeywords = []string{"amd64", "x86_64"}
	case "arm64":
		archKeywords = []string{"arm64", "aarch64"}
	default:
		archKeywords = []string{runtime.GOARCH}
	}

	for _, asset := range release.Assets {
		lower := strings.ToLower(asset.Name)
		if !strings.Contains(lower, "linux") {
			continue
		}
		for _, kw := range archKeywords {
			if strings.Contains(lower, kw) {
				return asset.BrowserDownloadURL
			}
		}
	}

	// Fallback: a bare "gopher" asset (single-platform releases)
	for _, asset := range release.Assets {
		if asset.Name == "gopher" {
			return asset.BrowserDownloadURL
		}
	}

	return ""
}

// isNewer returns true if latest semver tag is strictly greater than current.
func isNewer(latest, current string) bool {
	if latest == current {
		return false
	}
	lv := parseSemver(latest)
	cv := parseSemver(current)
	if lv == nil || cv == nil {
		return latest != current
	}
	return compareSemver(*lv, *cv) > 0
}

func parseSemver(tag string) *semverParts {
	tag = strings.TrimSpace(strings.TrimPrefix(tag, "v"))
	mainAndPre := strings.SplitN(tag, "-", 2)
	parts := strings.Split(mainAndPre[0], ".")
	if len(parts) != 3 {
		return nil
	}

	parsed := &semverParts{}
	if !parseUint(parts[0], &parsed.major) || !parseUint(parts[1], &parsed.minor) || !parseUint(parts[2], &parsed.patch) {
		return nil
	}

	if len(mainAndPre) == 2 {
		parsed.prerelease = strings.ToLower(mainAndPre[1])
	}

	return parsed
}

func parseUint(s string, out *int) bool {
	if s == "" {
		return false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
		n = n*10 + int(s[i]-'0')
	}
	*out = n
	return true
}

func compareSemver(a, b semverParts) int {
	if a.major != b.major {
		if a.major > b.major {
			return 1
		}
		return -1
	}
	if a.minor != b.minor {
		if a.minor > b.minor {
			return 1
		}
		return -1
	}
	if a.patch != b.patch {
		if a.patch > b.patch {
			return 1
		}
		return -1
	}

	// Stable release is newer than prerelease for equal X.Y.Z.
	if a.prerelease == "" && b.prerelease != "" {
		return 1
	}
	if a.prerelease != "" && b.prerelease == "" {
		return -1
	}
	if a.prerelease == b.prerelease {
		return 0
	}

	return comparePrerelease(a.prerelease, b.prerelease)
}

func comparePrerelease(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	maxLen := len(as)
	if len(bs) > maxLen {
		maxLen = len(bs)
	}

	for i := 0; i < maxLen; i++ {
		if i >= len(as) {
			return -1
		}
		if i >= len(bs) {
			return 1
		}

		ai, an := parseNumericIdentifier(as[i])
		bi, bn := parseNumericIdentifier(bs[i])

		if an && bn {
			if ai > bi {
				return 1
			}
			if ai < bi {
				return -1
			}
			continue
		}

		if an && !bn {
			return -1
		}
		if !an && bn {
			return 1
		}

		if as[i] > bs[i] {
			return 1
		}
		if as[i] < bs[i] {
			return -1
		}
	}

	return 0
}

func parseNumericIdentifier(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}

func isPrereleaseTag(version string) bool {
	v := strings.TrimPrefix(strings.TrimSpace(version), "v")
	return strings.Contains(v, "-")
}
