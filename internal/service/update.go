package service

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	osuser "os/user"
	"runtime"
	"strings"
	"time"

	"github.com/smalex-z/gopher/internal/build"
	"github.com/smalex-z/gopher/internal/db"
)

const githubRepo = "smalex-z/gopher"

type UpdateService struct{}

type UpdateInfo struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	UpdateAvailable bool   `json:"update_available"`
	Channel         string `json:"channel"`
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

// inferChannelFromVersion derives a sensible default channel from the running
// binary's version tag so that, e.g., a beta build defaults to the beta stream
// without requiring any explicit configuration.
func inferChannelFromVersion(version string) string {
	v := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(version), "v"))
	idx := strings.Index(v, "-")
	if idx < 0 {
		return "stable"
	}
	pre := v[idx+1:]
	if strings.Contains(pre, "alpha") {
		return "alpha"
	}
	if strings.Contains(pre, "beta") {
		return "beta"
	}
	// Unknown pre-release label — use alpha (most permissive) so the user
	// doesn't miss updates.
	return "alpha"
}

func settingsChannel() string {
	settings, err := db.GetSettings()
	if err == nil && settings != nil && settings.UpdateChannel != "" {
		return settings.UpdateChannel
	}
	return inferChannelFromVersion(build.Version)
}

func (s *UpdateService) Check() (*UpdateInfo, error) {
	channel := settingsChannel()
	info := &UpdateInfo{
		CurrentVersion:  build.Version,
		LatestVersion:   build.Version,
		UpdateAvailable: false,
		Channel:         channel,
	}

	if build.Version == "dev" {
		return info, nil
	}

	release, err := fetchLatestReleaseForChannel(channel)
	if err != nil {
		return nil, err
	}

	info.LatestVersion = release.TagName
	info.UpdateAvailable = isNewer(release.TagName, build.Version)
	return info, nil
}

func (s *UpdateService) Apply() error {
	release, err := fetchLatestReleaseForChannel(settingsChannel())
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

	// Patch sudoers to ensure any newly-required commands are present.
	// Failures are non-fatal — the update still proceeds.
	if err := patchSudoers(); err != nil {
		log.Printf("WARN: could not patch sudoers: %v", err)
	}

	// Refresh fail2ban filter files so new jails/filters from this release are
	// picked up. Only runs if fail2ban is installed and active.
	if isCommandAvailable("fail2ban-client") {
		if err := writeLocalFile("/etc/fail2ban/filter.d/gopher-auth.conf", fail2banFilterConfig); err != nil {
			log.Printf("WARN: could not update gopher-auth fail2ban filter: %v", err)
		}
		sudo := privilegedCmdPrefix()
		reloadArgs := append(append([]string{}, sudo...), "fail2ban-client", "reload")
		if out, err := exec.Command(reloadArgs[0], reloadArgs[1:]...).CombinedOutput(); err != nil {
			log.Printf("WARN: fail2ban reload failed: %v — %s", err, strings.TrimSpace(string(out)))
		}
	}

	// Restart the service after a short delay so this HTTP response can be delivered
	go func() {
		time.Sleep(time.Second)
		restartArgs := append(append([]string{}, privilegedCmdPrefix()...), "systemctl", "restart", "gopher")
		_ = exec.Command(restartArgs[0], restartArgs[1:]...).Run()
	}()

	return nil
}

// patchSudoers rewrites /etc/sudoers.d/<user> with the full set of commands
// the gopher service needs. Uses "sudo -n tee" which is already granted in the
// existing sudoers file written by the initial install.
func patchSudoers() error {
	currentUser, err := osuser.Current()
	if err != nil {
		return fmt.Errorf("could not determine current user: %w", err)
	}
	username := currentUser.Username

	content := buildServiceSudoers(username)
	sudoersPath := "/etc/sudoers.d/" + username

	cmd := exec.Command("sudo", "-n", "tee", sudoersPath) // #nosec G204
	cmd.Stdin = strings.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tee sudoers: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("sudo", "-n", "chmod", "0440", sudoersPath).CombinedOutput(); err != nil { // #nosec G204
		return fmt.Errorf("chmod sudoers: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// buildServiceSudoers returns the complete sudoers content for the service user.
func buildServiceSudoers(username string) string {
	lines := []string{
		"# Gopher server - limited sudo access",
		username + " ALL=(ALL:ALL) NOPASSWD: /usr/bin/systemctl, /bin/systemctl",
		username + " ALL=(ALL:ALL) NOPASSWD: /usr/bin/tee, /bin/tee",
		username + " ALL=(ALL:ALL) NOPASSWD: /usr/bin/mkdir, /bin/mkdir",
		username + " ALL=(ALL:ALL) NOPASSWD: /usr/bin/pkill, /bin/pkill",
		username + " ALL=(ALL:ALL) NOPASSWD: /bin/mv, /usr/bin/mv",
		username + " ALL=(ALL:ALL) NOPASSWD: /bin/rm, /usr/bin/rm",
		username + " ALL=(ALL:ALL) NOPASSWD: /usr/bin/chown, /bin/chown",
		username + " ALL=(ALL:ALL) NOPASSWD: /bin/chmod, /usr/bin/chmod",
		username + " ALL=(ALL:ALL) NOPASSWD: /usr/sbin/iptables, /sbin/iptables",
		username + " ALL=(ALL:ALL) NOPASSWD: /usr/sbin/iptables-save, /sbin/iptables-save",
		username + " ALL=(ALL:ALL) NOPASSWD: /usr/sbin/iptables-restore, /sbin/iptables-restore",
		username + " ALL=(ALL:ALL) NOPASSWD: /usr/sbin/ufw, /usr/bin/ufw",
		username + " ALL=(ALL:ALL) NOPASSWD: /usr/bin/apt-get, /bin/apt-get",
		username + " ALL=(ALL:ALL) NOPASSWD: /bin/bash, /usr/bin/bash",
		username + " ALL=(ALL:ALL) NOPASSWD: /usr/bin/curl, /bin/curl",
		username + " ALL=(ALL:ALL) NOPASSWD: /usr/bin/fail2ban-client, /usr/local/bin/fail2ban-client",
		username + " ALL=(ALL:ALL) NOPASSWD: /usr/bin/journalctl, /bin/journalctl",
		// Required for the post-upgrade self-heal in EnsureJumpboxUser:
		// legacy installs that pre-date the gopher-jump security split
		// don't have the user, and the running service needs to create it
		// without a password to avoid the silent fallback to the dashboard
		// user's authorized_keys.
		username + " ALL=(ALL:ALL) NOPASSWD: /usr/sbin/useradd, /usr/bin/useradd",
	}
	return strings.Join(lines, "\n") + "\n"
}

// releaseMatchesChannel returns true if the release should be considered for
// the given channel. Stable releases are always included; beta excludes alpha
// pre-releases; alpha includes everything that isn't a draft.
func releaseMatchesChannel(r *githubRelease, channel string) bool {
	if r.Draft || strings.TrimSpace(r.TagName) == "" {
		return false
	}
	sv := parseSemver(r.TagName)
	if sv == nil {
		return false
	}
	pre := sv.prerelease
	switch channel {
	case "alpha":
		return true
	case "beta":
		// stable releases and beta pre-releases; skip alpha
		return pre == "" || (strings.Contains(pre, "beta") && !strings.Contains(pre, "alpha"))
	default: // "stable"
		return pre == ""
	}
}

// fetchLatestReleaseForChannel returns the best (newest) release that matches
// the requested channel. For stable it uses the /releases/latest shortcut;
// for beta/alpha it pages through recent releases and filters client-side.
func fetchLatestReleaseForChannel(channel string) (*githubRelease, error) {
	if channel == "stable" {
		url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo)
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "gopher/"+build.Version)

		httpClient := &http.Client{Timeout: 10 * time.Second}
		resp, err := httpClient.Do(req)
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

	// beta / alpha — fetch list and pick the best matching release
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=30", githubRepo)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "gopher/"+build.Version)

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Do(req)
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
		if !releaseMatchesChannel(r, channel) {
			continue
		}
		if best == nil || isNewer(r.TagName, best.TagName) {
			best = r
		}
	}

	if best == nil {
		return nil, fmt.Errorf("no releases found for channel %q", channel)
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
		// Normalize dashes to dots so "alpha-5.1" and "alpha.5.1" compare equally,
		// and git-describe suffixes like "-6-gabcdef-dirty" parse as extra dot-separated identifiers.
		parsed.prerelease = strings.ToLower(strings.ReplaceAll(mainAndPre[1], "-", "."))
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

