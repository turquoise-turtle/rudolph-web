// Package depcheck holds checks on the module's dependencies.
package depcheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// No legacy dependencies before the first production deployment (owner, 2026-11-07): the current Go release, no
// deprecated module (the AWS SDK for Go v1 used to come in through Rudolph), nothing of it built.
func TestDependenciesAreCurrent(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	sawGo := false
	for _, line := range strings.Split(string(mod), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == "go" {
			sawGo = true
			parts := strings.Split(f[1], ".")
			if minor, _ := strconv.Atoi(parts[1]); parts[0] != "1" || minor < 27 {
				t.Errorf("go directive %s; want 1.27 or later", f[1])
			}
		}
	}
	if !sawGo {
		t.Error("go.mod has no go directive")
	}
	for _, deprecated := range []string{"github.com/golang/protobuf ", "github.com/aws/aws-sdk-go "} {
		if strings.Contains(string(mod), deprecated) {
			t.Errorf("go.mod requires the deprecated module %s", strings.TrimSpace(deprecated))
		}
	}
	cmd := exec.Command("go", "list", "-deps", "-test", "-tags", "integration", "-f", "{{.ImportPath}}", "./...")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, p := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(p, "github.com/aws/aws-sdk-go/") {
			t.Errorf("the AWS SDK for Go v1 is still built: %s", p)
			break
		}
	}
}
