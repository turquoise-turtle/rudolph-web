package ddblocal

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// testdata/rudolph/dynamodb.tf must be Rudolph's current table definition, or the data layer is tested against
// indexes production does not have.
func TestVendoredTableDefinitionMatchesRudolph(t *testing.T) {
	dir := os.Getenv("RUDOLPH_DIR")
	if dir == "" {
		dir = filepath.Join(repoRoot(), "..", "rudolph")
	}
	upstream, err := os.ReadFile(filepath.Join(dir, "deployments", "terraform_modules", "santa_api", "modules", "store", "dynamodb.tf"))
	if err != nil {
		t.Skipf("Rudolph checkout not found (set RUDOLPH_DIR): %v", err)
	}
	vendored, err := os.ReadFile(StoreTerraformFile())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(upstream, vendored) {
		t.Errorf("%s differs from Rudolph's copy in %s: copy it over and check what changed", StoreTerraformFile(), dir)
	}
}

// testdata/rudolph/events.tf must be Rudolph's current events table definition.
func TestVendoredEventsTableDefinitionMatchesRudolph(t *testing.T) {
	dir := os.Getenv("RUDOLPH_DIR")
	if dir == "" {
		dir = filepath.Join(repoRoot(), "..", "rudolph")
	}
	upstream, err := os.ReadFile(filepath.Join(dir, "deployments", "terraform_modules", "santa_api", "modules", "store", "events.tf"))
	if err != nil {
		t.Skipf("Rudolph checkout not found (set RUDOLPH_DIR): %v", err)
	}
	vendored, err := os.ReadFile(EventsTerraformFile())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(upstream, vendored) {
		t.Errorf("%s differs from Rudolph's copy in %s: copy it over and check what changed", EventsTerraformFile(), dir)
	}
}
