package deployments

// The minimum Santa version became Rudolph's stored setting (Rudolph 05f9516, its upgrade step 32), changed with
// `rudolph fleet min-santa-version` or the form on /admin/config (owner decisions for the editable minimum). So:
//
//   - The Terraform min_santa_version variable is gone from the module and the example environment: no declaration,
//     no module argument, no RUDOLPH_WEB_MIN_SANTA_VERSION in the function's environment, no value in
//     config.auto.tfvars.json and no row in the example's README.
//   - No IAM change: the web role already reads the store table (ReadRudolphStore: GetItem, not limited by partition)
//     and, with editing, updates the FleetHealth partition (WriteRudolphRules: UpdateItem, leading key "FleetHealth"
//     exactly), which is all fleethealth.SetMinSantaVersion and ClearMinSantaVersion do, and appends audit entries
//     (AppendRudolphAudit: PutItem on Audit#*). iam.tf's list of what each partition holds names the minimum Santa
//     version beside the holiday.
//   - README.md and docs/deploy.md each have an "Upgrading: editable minimum Santa version" section. docs/deploy.md's
//     gives the order as a numbered list: 1. deploy Rudolph's upgrade step 32 first; 2. remove min_santa_version from
//     _variables.tf, main.tf and config.auto.tfvars.json, which envcheck requires; 3. if the environment set a value
//     other than the default, set it once with `rudolph fleet min-santa-version` or the form on /admin/config. It says
//     there is no IAM change. README's links to it. Neither document still documents RUDOLPH_WEB_MIN_SANTA_VERSION as
//     a setting, tells anyone to add min_santa_version, or, outside upgrade notes, points at Rudolph's retired
//     FLEET_MIN_SANTA_VERSION.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestMinSantaVersionVariableIsGone(t *testing.T) {
	for _, dir := range []string{moduleDir, exampleDir} {
		for _, f := range loadTerraform(t, dir) {
			for _, gone := range []string{"min_santa_version", "RUDOLPH_WEB_MIN_SANTA_VERSION"} {
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
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg["min_santa_version"]; ok {
		t.Error("the example config.auto.tfvars.json still sets min_santa_version")
	}
	readme, err := os.ReadFile(filepath.Join(exampleDir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(readme), "| `min_santa_version`") {
		t.Error("the example environment's README still lists the min_santa_version variable")
	}
}

func TestEditing_MinSantaVersionNeedsNoNewGrant(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	statements := map[string]string{}
	for _, doc := range blocks(module, "data", "aws_iam_policy_document") {
		for _, st := range nested(doc, "statement") {
			src := bodySource(t, module, st)
			if m := regexp.MustCompile(`sid\s*=\s*"(\w+)"`).FindStringSubmatch(src); m != nil {
				statements[m[1]] = src
			}
		}
		for _, dyn := range nested(doc, "dynamic") {
			src := bodySource(t, module, dyn)
			for _, sid := range []string{"WriteRudolphRules", "AppendRudolphAudit"} {
				if strings.Contains(src, `"`+sid+`"`) {
					statements[sid] = src
				}
			}
		}
	}
	read := statements["ReadRudolphStore"]
	if !strings.Contains(read, `"dynamodb:GetItem"`) || strings.Contains(read, "LeadingKeys") {
		t.Errorf("ReadRudolphStore must allow GetItem on the store table by any key, FleetHealth / Config included:\n%s", read)
	}
	write := statements["WriteRudolphRules"]
	i := strings.Index(write, `sid              = "WriteRudolphRules"`)
	if i < 0 {
		i = strings.Index(write, `"WriteRudolphRules"`)
	}
	entry := write[i:]
	if end := strings.Index(entry, "},"); end >= 0 {
		entry = entry[:end]
	}
	if !strings.Contains(entry, `"dynamodb:UpdateItem"`) || !strings.Contains(entry, `"FleetHealth"`) {
		t.Errorf("WriteRudolphRules must allow UpdateItem with the leading key \"FleetHealth\":\n%s", entry)
	}
	if !strings.Contains(statements["AppendRudolphAudit"], `"Audit#*"`) {
		t.Error("AppendRudolphAudit no longer appends to Audit#*")
	}
	for _, f := range module {
		if strings.HasSuffix(f.path, "iam.tf") && !strings.Contains(string(f.src), "minimum Santa version") {
			t.Errorf("%s does not say the FleetHealth partition holds the minimum Santa version as well as the holiday", f.path)
		}
	}
}

// numberedItems returns the items of the first numbered list in text. Markdown here is not hard-wrapped, so an item is
// its line plus any indented lines under it; blank lines may separate items.
func numberedItems(text string) []string {
	var items []string
	item := regexp.MustCompile(`^\d+\.\s`)
	for _, line := range strings.Split(text, "\n") {
		switch {
		case item.MatchString(line):
			items = append(items, line)
		case len(items) == 0, strings.TrimSpace(line) == "":
		case strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t"):
			items[len(items)-1] += " " + strings.TrimSpace(line)
		default:
			return items
		}
	}
	return items
}

func TestDocs_EditableMinSantaVersionUpgrade(t *testing.T) {
	const heading = "Upgrading: editable minimum Santa version"
	docs := map[string]string{}
	for _, path := range []string{"../README.md", "../docs/deploy.md"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		docs[path] = string(raw)
	}

	deploy := markdownSections(docs["../docs/deploy.md"])[heading]
	if deploy == "" {
		t.Fatalf("docs/deploy.md has no %q section", heading)
	}
	steps := numberedItems(deploy)
	if len(steps) < 3 {
		t.Fatalf("docs/deploy.md's %q section gives %d numbered steps, want the three in order:\n%s", heading, len(steps), deploy)
	}
	for i, wants := range [][]string{
		{"step 32"},
		{"min_santa_version", "_variables.tf", "main.tf", "config.auto.tfvars.json", "envcheck"},
		{"rudolph fleet min-santa-version", "/admin/config"},
	} {
		for _, w := range wants {
			if !strings.Contains(steps[i], w) {
				t.Errorf("docs/deploy.md's upgrade step %d does not mention %q:\n%s", i+1, w, steps[i])
			}
		}
	}
	if !strings.Contains(deploy, "IAM") {
		t.Error("docs/deploy.md's upgrade section does not say whether IAM changes (it does not)")
	}

	readme := markdownSections(docs["../README.md"])[heading]
	if readme == "" {
		t.Fatalf("README.md has no %q section", heading)
	}
	for _, w := range []string{"step 32", "min_santa_version", "rudolph fleet min-santa-version", "docs/deploy.md#upgrading-editable-minimum-santa-version"} {
		if !strings.Contains(readme, w) {
			t.Errorf("README.md's %q section does not mention %q", heading, w)
		}
	}

	for path, doc := range docs {
		if strings.Contains(doc, "| `RUDOLPH_WEB_MIN_SANTA_VERSION`") {
			t.Errorf("%s still documents RUDOLPH_WEB_MIN_SANTA_VERSION as a setting", path)
		}
		if strings.Contains(doc, `"min_santa_version": "`) || strings.Contains(doc, "min_santa_version = var.min_santa_version") {
			t.Errorf("%s still tells an environment to add min_santa_version", path)
		}
		for h, text := range markdownSections(doc) {
			if strings.HasPrefix(h, "Upgrading") {
				continue
			}
			if strings.Contains(text, "FLEET_MIN_SANTA_VERSION") {
				t.Errorf("%s, section %q, points at FLEET_MIN_SANTA_VERSION, which Rudolph no longer reads", path, h)
			}
		}
	}
}
