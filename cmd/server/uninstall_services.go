package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/smalex-z/gopher/internal/paths"
	"github.com/smalex-z/gopher/internal/service"
)

func resetCaddyManagedConfig() error {
	caddyfile := paths.CaddyfilePath
	if _, err := os.Stat(caddyfile); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to access Caddyfile: %w", err)
	}
	if err := backupFile(caddyfile, caddyfile+".gopher-backup"); err != nil {
		return fmt.Errorf("failed to backup Caddyfile: %w", err)
	}

	// Gopher-managed routing lives entirely in conf.d/gopher-*.caddy (separate
	// files) plus the import line in the Caddyfile; the user's own config is the
	// custom-marker block. The clean reset keeps ONLY the user's config —
	// service.ExtractUserCaddyConfig strips every trace of Gopher (managed header,
	// boilerplate comments, the conf.d import, and any stray/stacked managed
	// headers an old reconcile may have accumulated inside the custom block) —
	// and deletes the managed conf.d files.
	data, err := os.ReadFile(caddyfile)
	if err != nil {
		return fmt.Errorf("failed to read Caddyfile: %w", err)
	}
	userConfig := service.ExtractUserCaddyConfig(string(data))
	if strings.TrimSpace(userConfig) == "" {
		// Nothing of the operator's left — drop the file entirely rather than
		// leaving an empty Gopher-owned Caddyfile behind.
		_ = os.Remove(caddyfile)
	} else if err := os.WriteFile(caddyfile, []byte(userConfig), 0644); err != nil {
		return fmt.Errorf("failed to rewrite Caddyfile: %w", err)
	}
	managed, _ := filepath.Glob(filepath.Join(paths.CaddyConfDir, "gopher-*.caddy"))
	for _, f := range managed {
		_ = os.Remove(f)
	}
	return nil
}

// detectCustomConfig reports whether the operator has any config of their own
// under /etc/gopher: Caddy site blocks in the Caddyfile's custom section, or
// rathole services in server.toml's custom block. Drives the uninstall prompt —
// when everything is Gopher-managed there is nothing to preserve and nothing
// worth asking about.
func detectCustomConfig() (customCaddy, customRathole bool) {
	if data, err := os.ReadFile(paths.CaddyfilePath); err == nil {
		customCaddy = strings.TrimSpace(service.ExtractUserCaddyConfig(string(data))) != ""
	}
	if data, err := os.ReadFile(paths.RatholeConfig); err == nil {
		customRathole = ratholeCustomBlockHasContent(string(data))
	}
	return customCaddy, customRathole
}

// ratholeCustomBlockHasContent reports whether server.toml's custom-config
// marker block contains anything beyond comments and blank lines.
func ratholeCustomBlockHasContent(content string) bool {
	const begin = "# ===== BEGIN CUSTOM CONFIGURATION ====="
	const end = "# ===== END CUSTOM CONFIGURATION ====="
	i := strings.Index(content, begin)
	if i == -1 {
		return false
	}
	body := content[i+len(begin):]
	if j := strings.Index(body, end); j != -1 {
		body = body[:j]
	}
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if t != "" && !strings.HasPrefix(t, "#") {
			return true
		}
	}
	return false
}

func removeCaddyCompletely() error {
	// caddy is bundled into the gopher binary and supervised as a child — there's
	// no apt package or caddy.service to remove. Drop its config tree; certs in
	// /var/lib/gopher/caddy go with the data-directory removal.
	caddyDir := filepath.Join(paths.ConfigDir, "caddy")
	if err := os.RemoveAll(caddyDir); err != nil {
		return fmt.Errorf("failed to remove %s: %w", caddyDir, err)
	}
	return nil
}

func resetRatholeManagedConfig() error {
	ratholePath := paths.RatholeConfig
	if _, err := os.Stat(ratholePath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to access rathole config: %w", err)
	}
	if err := backupFile(ratholePath, ratholePath+".gopher-backup"); err != nil {
		return fmt.Errorf("failed to backup rathole config: %w", err)
	}
	// rathole keeps managed entries and the custom block in the SAME file (no
	// conf.d split), so we still strip by markers. No reload kick — rathole is a
	// gopher child and gopher is being uninstalled.
	if err := runPythonScript(stripRatholePython, ratholePath); err != nil {
		return err
	}
	// The non-custom remainder ([server], [server.transport] noise) is Gopher's,
	// not the operator's. If there's no custom-services block left, drop the file
	// entirely rather than leave a Gopher-owned server.toml (with our noise key)
	// behind.
	if data, err := os.ReadFile(ratholePath); err == nil {
		if !strings.Contains(string(data), "# ===== BEGIN CUSTOM CONFIGURATION =====") {
			_ = os.Remove(ratholePath)
		}
	}
	return nil
}

func removeRatholeCompletely() error {
	// rathole is bundled into the gopher binary and supervised — no
	// rathole-server.service and no /usr/local/bin/rathole. Just drop its config.
	ratholeDir := filepath.Join(paths.ConfigDir, "rathole")
	if err := os.RemoveAll(ratholeDir); err != nil {
		return fmt.Errorf("failed to remove %s: %w", ratholeDir, err)
	}
	return nil
}

func backupFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

func runPythonScript(script string, args ...string) error {
	pythonPath, err := exec.LookPath("python3")
	if err != nil {
		return fmt.Errorf("python3 is required for config reset: %w", err)
	}
	cmdArgs := append([]string{"-"}, args...)
	cmd := exec.Command(pythonPath, cmdArgs...)
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("config reset helper failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

const stripRatholePython = `import sys, re

path = sys.argv[1]

BEGIN = "# ===== BEGIN CUSTOM CONFIGURATION ====="
SHORT_HEX = re.compile(r'^[0-9a-f]{16}$')
UUID_PAT  = re.compile(r'^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$')

def is_gopher_section(line):
    s = line.strip()
    if s == "[server.services.placeholder]":
        return True
    if s.startswith("[server.services.machine-") and s.endswith("-ssh]"):
        tok = s[len("[server.services.machine-"):-len("-ssh]")]
        return bool(SHORT_HEX.match(tok) or UUID_PAT.match(tok))
    if s.startswith("[server.services.tunnel-") and s.endswith("]"):
        tok = s[len("[server.services.tunnel-"):-1]
        return bool(SHORT_HEX.match(tok) or UUID_PAT.match(tok))
    return False

def strip_gopher_sections(text):
    lines  = text.split("\n")
    result = []
    skip   = False
    for line in lines:
        s = line.strip()
        if is_gopher_section(s):
            skip = True
            continue
        if skip and s.startswith("["):
            skip = False
        if not skip:
            result.append(line)
    return "\n".join(result)

with open(path) as fh:
    content = fh.read()

custom_section = ""
if BEGIN in content:
    b_idx          = content.index(BEGIN)
    custom_section = content[b_idx:]
    content        = content[:b_idx]

header = strip_gopher_sections(content).rstrip("\n") + "\n"
if custom_section.strip():
    result = header + "\n" + custom_section
else:
    result = header

with open(path, "w") as fh:
    fh.write(result.strip() + "\n")
`
