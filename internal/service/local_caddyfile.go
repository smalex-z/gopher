package service

import (
	"fmt"
	"strings"
)

func buildLocalCaddyfile(domain string) string {
	return fmt.Sprintf(`{
    email admin@%s
}

router.%s {
    reverse_proxy localhost:8080
}

# ===== BEGIN CUSTOM CONFIGURATION =====
# Everything below this line will NOT be overwritten on local setup.
# Add any custom Caddy directives or site blocks here.
# ===== END CUSTOM CONFIGURATION =====
`, domain, domain)
}

// removeCaddyBlock removes a site block starting with `host {` from a Caddyfile.
func removeCaddyBlock(content, host string) string {
	lines := strings.Split(content, "\n")
	result := make([]string, 0, len(lines))
	skip := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !skip && strings.HasPrefix(trimmed, host) && strings.HasSuffix(trimmed, "{") {
			skip = true
			depth = 1
			continue
		}
		if skip {
			for _, ch := range line {
				if ch == '{' {
					depth++
       return fmt.Sprintf(`{
				} else if ch == '}' {
					depth--
				}
			}
			if depth <= 0 {
				skip = false
			}
			continue
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}

// mergeCaddyfile builds a new Caddyfile that places Gopher's managed
// dashboard block above the custom section.
//
// If the file already has custom-section markers (a previous Gopher run):
//   - Content above BEGIN is Gopher's managed zone — update dashboard block there.
//   - Content between BEGIN/END is the user's zone — leave it untouched.
//
// If the file has NO markers yet (first time Gopher touches it):
//   - Treat ALL existing content as user config → wrap it in the custom section.
//   - Place Gopher's dashboard block ABOVE the custom section.
func mergeCaddyfile(existing, domain string) string {
	       const beginMarker = "# ===== BEGIN CUSTOM CONFIGURATION ====="
	       const endMarker = "# ===== END CUSTOM CONFIGURATION ====="
	       dashboardBlock := fmt.Sprintf("router.%s {\n    reverse_proxy localhost:8080\n}\n", domain)

	       // Remove extra global blocks from existing content
	       cleanedExisting := removeExtraGlobalBlocks(existing)

	       if idx := strings.Index(cleanedExisting, beginMarker); idx != -1 {
		       // Markers already present: managed zone is everything before BEGIN.
		       managedZone := strings.TrimSpace(cleanedExisting[:idx])
		       customSection := cleanedExisting[idx:] // preserve from BEGIN to end verbatim
		       if strings.Contains(managedZone, fmt.Sprintf("router.%s", domain)) {
			       // Dashboard block already in managed zone — nothing to do.
			       return cleanedExisting
		       }
		       return managedZone + "\n\n" + dashboardBlock + "\n" + customSection
	       }

	       // No markers yet: move all existing content into the custom section.
	       trimmed := strings.TrimRight(cleanedExisting, "\n")
	       return dashboardBlock + "\n" +
		       beginMarker + "\n" +
		       "# Everything below this line will NOT be overwritten.\n" +
		       "# Add your own Caddy site blocks here.\n" +
		       trimmed + "\n" +
		       endMarker + "\n"
}
