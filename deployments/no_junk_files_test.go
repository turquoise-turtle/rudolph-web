package deployments

// Finder's .DS_Store files are not part of the repo (review): two were committed under deployments/.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepo_NoDSStoreFiles(t *testing.T) {
	out, err := exec.Command("git", "-C", "..", "ls-files").Output()
	if err != nil {
		t.Skipf("git ls-files: %v", err)
	}
	for _, f := range strings.Split(string(out), "\n") {
		if filepath.Base(f) == ".DS_Store" {
			t.Errorf("%s is tracked", f)
		}
	}
	ignore, err := os.ReadFile(filepath.Join("..", ".gitignore"))
	if err != nil || !strings.Contains(string(ignore), ".DS_Store") {
		t.Error(".gitignore does not ignore .DS_Store")
	}
}
