package config

import (
	"fmt"
	"regexp"
	"strings"
)

// ParseResult contains extracted config entries and any errors found
type ParseResult struct {
	Machines     map[string]*GopherEntry
	Tunnels      map[string]*GopherEntry
	Errors       []string
	DuplicateIDs []string
}

// parseRatholeConfig extracts gopher-marked entries and detects duplicates/errors
func parseRatholeConfig(configContent string) *ParseResult {
	result := &ParseResult{
		Machines:     make(map[string]*GopherEntry),
		Tunnels:      make(map[string]*GopherEntry),
		Errors:       []string{},
		DuplicateIDs: []string{},
	}

	lines := strings.Split(configContent, "\n")
	startPattern := regexp.MustCompile(`^#\s*gopher-(machine|tunnel)-start:\s*(.+?)\s*$`)
	endPattern := regexp.MustCompile(`^#\s*gopher-(machine|tunnel)-end:\s*(.+?)\s*$`)

	seenMachines := make(map[string]int)
	seenTunnels := make(map[string]int)

	var currentEntry *GopherEntry
	var currentContent []string
	var currentType, currentID string

	for lineNo, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Check for start marker
		if matches := startPattern.FindStringSubmatch(trimmed); matches != nil {
			entryType := matches[1]
			id := matches[2]

			// Track duplicates
			if entryType == "machine" {
				seenMachines[id]++
				if seenMachines[id] > 1 {
					result.DuplicateIDs = append(result.DuplicateIDs, fmt.Sprintf("machine-%s", id))
					result.Errors = append(result.Errors, fmt.Sprintf("line %d: duplicate machine ID: %s", lineNo+1, id))
				}
			} else {
				seenTunnels[id]++
				if seenTunnels[id] > 1 {
					result.DuplicateIDs = append(result.DuplicateIDs, fmt.Sprintf("tunnel-%s", id))
					result.Errors = append(result.Errors, fmt.Sprintf("line %d: duplicate tunnel ID: %s", lineNo+1, id))
				}
			}

			// Detect nesting
			if currentEntry != nil {
				result.Errors = append(result.Errors,
					fmt.Sprintf("line %d: nested marker (previous %s-%s not closed)", lineNo+1, currentType, currentID))
			}

			currentEntry = &GopherEntry{Type: entryType, ID: id}
			currentType = entryType
			currentID = id
			currentContent = []string{}
			continue
		}

		// Check for end marker
		if matches := endPattern.FindStringSubmatch(trimmed); matches != nil {
			endType := matches[1]
			endID := matches[2]

			if currentEntry == nil {
				result.Errors = append(result.Errors,
					fmt.Sprintf("line %d: orphaned end marker for %s-%s", lineNo+1, endType, endID))
				continue
			}

			// Validate matching IDs
			if currentEntry.Type != endType || currentEntry.ID != endID {
				result.Errors = append(result.Errors,
					fmt.Sprintf("line %d: marker mismatch (%s-%s start vs %s-%s end)", lineNo+1, currentType, currentID, endType, endID))
			}

			// Extract token and bind_addr from content
			content := strings.Join(currentContent, "\n")
			tokenRe := regexp.MustCompile(`token\s*=\s*"([^"]*)"`)
			if m := tokenRe.FindStringSubmatch(content); len(m) > 1 {
				currentEntry.Token = m[1]
			}
			bindAddrRe := regexp.MustCompile(`bind_addr\s*=\s*"([^"]*)"`)
			if m := bindAddrRe.FindStringSubmatch(content); len(m) > 1 {
				currentEntry.BindAddr = m[1]
			}

			// Store the entry
			if currentEntry.Type == "machine" {
				result.Machines[currentEntry.ID] = currentEntry
			} else {
				result.Tunnels[currentEntry.ID] = currentEntry
			}
			currentEntry = nil
			continue
		}

		// Accumulate content
		if currentEntry != nil {
			currentContent = append(currentContent, line)
		}
	}

	// Detect unclosed markers
	if currentEntry != nil {
		result.Errors = append(result.Errors,
			fmt.Sprintf("unclosed marker: %s-%s never closed", currentType, currentID))
	}

	return result
}
