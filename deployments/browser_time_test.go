package deployments

// Times are converted to each viewer's time zone in the browser (owner request after the first production
// deployment), so the server-side display_time_zone setting is gone: the module, the function's environment and the
// example environment no longer carry it.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDisplayTimeZoneIsGone(t *testing.T) {
	for _, dir := range []string{moduleDir, exampleDir} {
		for _, f := range loadTerraform(t, dir) {
			for _, gone := range []string{"display_time_zone", "RUDOLPH_WEB_DISPLAY_TIME_ZONE"} {
				if strings.Contains(string(f.src), gone) {
					t.Errorf("%s still mentions %s", f.path, gone)
				}
			}
		}
	}
	raw, err := os.ReadFile(filepath.Join(exampleDir, "config.auto.tfvars.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "display_time_zone") {
		t.Error("the example config.auto.tfvars.json still sets display_time_zone")
	}
}
