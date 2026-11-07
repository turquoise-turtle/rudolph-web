package deployments

import (
	"regexp"
	"strconv"
	"testing"
)

// Terraform itself: at least 1.10 (older releases are no longer maintained; owner, 2026-11-07).
func TestTerraformVersionIsCurrent(t *testing.T) {
	re := regexp.MustCompile(`required_version\s*=\s*">=\s*1\.(\d+)`)
	found := 0
	for _, dir := range []string{moduleDir, exampleDir} {
		for _, f := range loadTerraform(t, dir) {
			for _, m := range re.FindAllStringSubmatch(string(f.src), -1) {
				found++
				if minor, _ := strconv.Atoi(m[1]); minor < 10 {
					t.Errorf("%s: required_version >= 1.%s; want >= 1.10", f.path, m[1])
				}
			}
		}
	}
	if found == 0 {
		t.Error("no required_version found")
	}
}
