package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func resetCaddyManagedConfig() error {
	const caddyPath = "/etc/caddy/Caddyfile"
	if _, err := os.Stat(caddyPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to access Caddyfile: %w", err)
	}

	backupPath := caddyPath + ".gopher-backup"
	if err := backupFile(caddyPath, backupPath); err != nil {
		return fmt.Errorf("failed to backup Caddyfile: %w", err)
	}
	if err := runPythonScript(stripCaddyPython, caddyPath, ""); err != nil {
		return err
	}

	if systemctlPath, err := exec.LookPath("systemctl"); err == nil {
		runCommandBestEffort(systemctlPath, "reload", "caddy")
	}
	return nil
}

func removeCaddyCompletely() error {
	if systemctlPath, err := exec.LookPath("systemctl"); err == nil {
		runCommandBestEffort(systemctlPath, "stop", "caddy")
		runCommandBestEffort(systemctlPath, "disable", "caddy")
	}
	if aptGetPath, err := exec.LookPath("apt-get"); err == nil {
		runCommandBestEffort(aptGetPath, "purge", "-y", "caddy")
	}
	if err := os.RemoveAll("/etc/caddy"); err != nil {
		return fmt.Errorf("failed to remove /etc/caddy: %w", err)
	}
	return nil
}

func resetRatholeManagedConfig() error {
	const ratholePath = "/etc/rathole/server.toml"
	if _, err := os.Stat(ratholePath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to access rathole config: %w", err)
	}

	backupPath := ratholePath + ".gopher-backup"
	if err := backupFile(ratholePath, backupPath); err != nil {
		return fmt.Errorf("failed to backup rathole config: %w", err)
	}
	if err := runPythonScript(stripRatholePython, ratholePath); err != nil {
		return err
	}

	if pkillPath, err := exec.LookPath("pkill"); err == nil {
		if exec.Command(pkillPath, "-HUP", "-x", "rathole").Run() == nil {
			return nil
		}
	}
	if systemctlPath, err := exec.LookPath("systemctl"); err == nil {
		runCommandBestEffort(systemctlPath, "restart", "rathole-server")
	}
	return nil
}

func removeRatholeCompletely() error {
	if systemctlPath, err := exec.LookPath("systemctl"); err == nil {
		runCommandBestEffort(systemctlPath, "stop", "rathole-server")
		runCommandBestEffort(systemctlPath, "disable", "rathole-server")
	}
	_, _ = removeFileIfExists("/etc/systemd/system/rathole-server.service")
	if systemctlPath, err := exec.LookPath("systemctl"); err == nil {
		runCommandBestEffort(systemctlPath, "daemon-reload")
	}
	_, _ = removeFileIfExists("/usr/local/bin/rathole")
	if err := os.RemoveAll("/etc/rathole"); err != nil {
		return fmt.Errorf("failed to remove /etc/rathole: %w", err)
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

const stripCaddyPython = `import sys, re

path = sys.argv[1]
domain = sys.argv[2] if len(sys.argv) > 2 else ""

BEGIN = "# ===== BEGIN CUSTOM CONFIGURATION ====="
END   = "# ===== END CUSTOM CONFIGURATION ====="

with open(path) as fh:
    content = fh.read()

if not domain:
    m = re.search(r'^router\.(\S+)\s*\{', content, re.MULTILINE)
    if m:
        domain = m.group(1)

def remove_block(text, host_prefix):
    lines  = text.split("\n")
    result = []
    depth  = 0
    skip   = False
    for line in lines:
        stripped = line.strip()
        if not skip and stripped.startswith(host_prefix) and stripped.endswith("{"):
            skip  = True
            depth = 1
            continue
        if skip:
            depth += line.count("{") - line.count("}")
            if depth <= 0:
                skip = False
            continue
        result.append(line)
    return "\n".join(result)

user_lines = []
if BEGIN in content and END in content:
    b_idx        = content.index(BEGIN)
    e_idx        = content.index(END) + len(END)
    section_body = content[b_idx + len(BEGIN) : content.index(END)]
    raw_lines    = section_body.split("\n")

    skip_comments = {
        "# Everything below this line will NOT be overwritten on local setup.",
        "# Add any custom Caddy directives or site blocks here.",
        "# Everything below this line will NOT be overwritten.",
        "# Add your own Caddy site blocks here.",
    }

    if domain:
        tunnel_header_re = re.compile(r'^[\w\-\*]+\.' + re.escape(domain) + r'\s*\{$')
        filtered = []
        depth = 0
        skip  = False
        for line in raw_lines:
            stripped = line.strip()
            if stripped in skip_comments:
                continue
            if not skip and tunnel_header_re.match(stripped):
                skip  = True
                depth = 1
                continue
            if skip:
                depth += line.count("{") - line.count("}")
                if depth <= 0:
                    skip = False
                continue
            filtered.append(line)
        user_lines = filtered
    else:
        user_lines = [l for l in raw_lines if l.strip() not in skip_comments]

    before  = content[:b_idx].rstrip()
    after   = content[e_idx:].lstrip("\n")
    content = (before + "\n" + after) if after else (before + "\n")

if domain:
    content = remove_block(content, f"router.{domain}")

preserved = "\n".join(user_lines).strip()
if preserved:
    content = content.rstrip("\n") + "\n\n" + preserved + "\n"

content = content.strip() + "\n"

with open(path, "w") as fh:
    fh.write(content)
`

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
