package config

import (
"fmt"
"regexp"
)

var subdomainRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9\-]{0,61}[a-z0-9])?$`)

func ValidateSubdomain(s string) error {
if !subdomainRegex.MatchString(s) {
return fmt.Errorf("invalid subdomain: must be lowercase alphanumeric and hyphens only")
}
return nil
}

func ValidatePort(p int) error {
if p < 1 || p > 65535 {
return fmt.Errorf("invalid port: must be between 1 and 65535")
}
return nil
}
