package deployments

// Terraform audit, review rounds 1 and 2 (2026-11-19). The static checks in terraform_audit_test.go read the
// configuration as text, which cannot show what a plan does: that a count written with `||` creates the log bucket
// without CloudFront, that web_acl_id comes out as the ACL's ARN, or that "${var.prefix}-..." is not a valid S3
// bucket name for a prefix such as my_org, which plans cleanly and only fails at apply. The TestModule_ tests run
// `terraform test` against the module with mock providers, so nothing talks to AWS.
//
// They are opt-in (RUDOLPH_WEB_TERRAFORM_TESTS=1, which `make tf-check` sets): the first run downloads
// hashicorp/aws, about 700 MB, so `go test ./...` and `make deploy`, which runs it, would otherwise fail offline and
// take minutes longer. All of them share one terraform init and one `terraform test` process; the plugin cache under
// the user's cache directory makes later runs quick.

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

const (
	moduleRoot        = moduleDir + "/rudolph_web"
	terraformTestsEnv = "RUDOLPH_WEB_TERRAFORM_TESTS"
)

// The prefixes existing or plausible deployments use that a naive "${var.prefix}-..." bucket name gets wrong:
// capitals and underscores, too long, and S3's reserved prefixes.
var bucketNamePrefixes = []string{
	"my_org",
	"Prod",
	strings.Repeat("abcdefghij", 4),
	"amzn-s3-demo-x",
	"xn--abc",
	"sthree-x",
	strings.Repeat("abcdefghij", 3) + "a",
	strings.Repeat("abcdefghi_", 5) + "ab",
	// Valid as they stand, but reserved once the name adds its hyphen: sthree-, amzn-s3-demo-, xn--.
	"sthree",
	"amzn-s3-demo",
	"xn-",
}

type featureCase struct {
	name                     string
	cloudfront, logging, waf bool
	warn                     bool
}

var featureCases = []featureCase{
	{"logging_without_cloudfront", false, true, false, true},
	{"waf_without_cloudfront", false, false, true, true},
	{"both_with_cloudfront", true, true, true, false},
	{"nothing_on", false, false, false, false},
}

type moduleTestRun struct {
	once    sync.Once
	skip    string
	fatal   string
	files   map[string]string // test file (tests/<name>.tftest.hcl) to its part of the output
	results map[string]bool   // test file to whether it passed
	all     string
}

var moduleTests moduleTestRun

// moduleTestResults runs every test file, committed and generated, in one terraform init and one terraform test,
// the first time a TestModule_ test asks.
func moduleTestResults(t *testing.T) *moduleTestRun {
	t.Helper()
	if os.Getenv(terraformTestsEnv) != "1" {
		t.Skipf("the module's terraform tests are opt-in: run `make tf-check`, or %s=1 go test ./deployments -run TestModule (needs terraform, and the network the first time)", terraformTestsEnv)
	}
	moduleTests.once.Do(func() { moduleTests.run(t) })
	if moduleTests.fatal != "" {
		t.Fatal(moduleTests.fatal)
	}
	return &moduleTests
}

func (m *moduleTestRun) run(t *testing.T) {
	terraform, err := exec.LookPath("terraform")
	if err != nil {
		m.fatal = fmt.Sprintf("%s=1 but terraform is not installed", terraformTestsEnv)
		return
	}
	dir, err := os.MkdirTemp("", "rudolph-web-module-")
	if err != nil {
		m.fatal = err.Error()
		return
	}
	// Removed when the package's tests finish; t.Cleanup would remove it after the first test.
	moduleTestDirs = append(moduleTestDirs, dir)

	err = filepath.WalkDir(moduleRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(moduleRoot, path)
		if d.IsDir() {
			if d.Name() == ".terraform" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dir, rel), 0o755)
		}
		if d.Name() == lockFile {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, rel), b, 0o644)
	})
	if err != nil {
		m.fatal = err.Error()
		return
	}
	// The example's lock file, where there is one, so the tests use the provider build deploys use.
	if b, err := os.ReadFile(filepath.Join(exampleDir, lockFile)); err == nil {
		if err := os.WriteFile(filepath.Join(dir, lockFile), b, 0o644); err != nil {
			m.fatal = err.Error()
			return
		}
	}

	mocks, err := mockProviders()
	if err != nil {
		m.fatal = err.Error()
		return
	}
	for i, prefix := range bucketNamePrefixes {
		src := mocks + moduleVariables(prefix, true, true, false) + bucketNameRun
		if err := os.WriteFile(filepath.Join(dir, "tests", fmt.Sprintf("gen_bucket_name_%d.tftest.hcl", i)), []byte(src), 0o644); err != nil {
			m.fatal = err.Error()
			return
		}
	}
	for _, c := range featureCases {
		src := mocks + moduleVariables("audit", c.cloudfront, c.logging, c.waf) + "\nrun \"plan\" {\n  command = plan\n}\n"
		if err := os.WriteFile(filepath.Join(dir, "tests", "gen_"+c.name+".tftest.hcl"), []byte(src), 0o644); err != nil {
			m.fatal = err.Error()
			return
		}
	}

	if out, err := terraformCmd(terraform, dir, "init", "-backend=false", "-input=false", "-no-color").CombinedOutput(); err != nil {
		m.fatal = fmt.Sprintf("terraform init in a copy of the module: %v\n%s", err, out)
		return
	}
	out, _ := terraformCmd(terraform, dir, "test", "-no-color").CombinedOutput()
	m.all = string(out)
	m.files, m.results = splitTestOutput(m.all)
}

var moduleTestDirs []string

func TestMain(m *testing.M) {
	code := m.Run()
	for _, d := range moduleTestDirs {
		os.RemoveAll(d)
	}
	os.Exit(code)
}

var testFileLine = regexp.MustCompile(`^(tests/[^ ]+\.tftest\.hcl)\.\.\. (in progress|tearing down|pass|fail|skip)\s*$`)

// splitTestOutput cuts terraform test's output into each file's part, from its "in progress" line to its result.
func splitTestOutput(out string) (map[string]string, map[string]bool) {
	files, results := map[string]string{}, map[string]bool{}
	current := ""
	for _, line := range strings.Split(out, "\n") {
		if m := testFileLine.FindStringSubmatch(line); m != nil {
			current = m[1]
			switch m[2] {
			case "pass":
				results[current] = true
			case "fail", "skip":
				results[current] = false
			}
		}
		if current != "" {
			files[current] += line + "\n"
		}
	}
	return files, results
}

// file returns a test file's output and result, failing the test when terraform test never got to it.
func (m *moduleTestRun) file(t *testing.T, name string) (string, bool) {
	t.Helper()
	out, seen := m.files[name]
	if !seen {
		t.Fatalf("terraform test did not run %s:\n%s", name, m.all)
	}
	return out, m.results[name]
}

func terraformCmd(terraform, dir string, args ...string) *exec.Cmd {
	cmd := exec.Command(terraform, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TF_IN_AUTOMATION=1", "TF_INPUT=0", "CHECKPOINT_DISABLE=1")
	if cache, err := os.UserCacheDir(); err == nil {
		plugins := filepath.Join(cache, "rudolph-web", "terraform-plugins")
		if err := os.MkdirAll(plugins, 0o755); err == nil {
			cmd.Env = append(cmd.Env, "TF_PLUGIN_CACHE_DIR="+plugins)
		}
	}
	return cmd
}

// mockProviders returns the committed test file's mock_provider blocks, so generated test files mock exactly as it does.
func mockProviders() (string, error) {
	path := filepath.Join(moduleRoot, "tests", "audit.tftest.hcl")
	src, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	f, diags := hclparse.NewParser().ParseHCL(src, path)
	if diags.HasErrors() {
		return "", fmt.Errorf("parse %s: %s", path, diags.Error())
	}
	var out string
	for _, b := range f.Body.(*hclsyntax.Body).Blocks {
		if b.Type == "mock_provider" {
			out += string(b.Range().SliceBytes(src)) + "\n\n"
		}
	}
	if out == "" {
		return "", fmt.Errorf("%s has no mock_provider blocks", path)
	}
	return out, nil
}

func moduleVariables(prefix string, cloudfront, logging, waf bool) string {
	return fmt.Sprintf(`
variables {
  prefix                    = %q
  region                    = "ap-southeast-2"
  aws_account_id            = "123456789012"
  tenant_id                 = "00000000-0000-0000-0000-000000000000"
  client_id                 = "00000000-0000-0000-0000-000000000001"
  dynamodb_table_name       = "audit_rudolph_store"
  lambda_zip                = "main.tf"
  enable_cloudfront         = %v
  enable_cloudfront_logging = %v
  enable_waf                = %v
}
`, prefix, cloudfront, logging, waf)
}

// S3's general purpose bucket naming rules: 3 to 63 lowercase letters, digits, dots and hyphens, starting and ending
// with a letter or digit, no "..", not an IP address, and none of the prefixes and suffixes S3 reserves. Only the
// bucket is planned: other names built from a long prefix (the IAM role's, the log deliveries') have limits of their
// own, which the provider checks at plan, and would hide what this run is about.
const bucketNameRun = `
run "bucket_name" {
  command = apply

  plan_options {
    target = [aws_s3_bucket.cloudfront_logs]
  }

  assert {
    condition = length(aws_s3_bucket.cloudfront_logs) == 1 && alltrue([
      for n in aws_s3_bucket.cloudfront_logs[*].bucket :
      can(regex("^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$", n)) &&
      !strcontains(n, "..") &&
      !can(regex("^[0-9]+\\.[0-9]+\\.[0-9]+\\.[0-9]+$", n)) &&
      !anytrue([for p in ["xn--", "sthree-", "amzn-s3-demo-"] : startswith(n, p)]) &&
      !anytrue([for s in ["-s3alias", "--ol-s3", ".mrap", "--x-s3"] : endswith(n, s)])
    ])
    error_message = "invalid S3 bucket name: ${try(aws_s3_bucket.cloudfront_logs[0].bucket, "(none)")}"
  }
}
`

// The switch combinations, web_acl_id, the bucket policy and the validations, from the committed test file.
func TestModule_TerraformTestFile(t *testing.T) {
	m := moduleTestResults(t)
	if out, ok := m.file(t, "tests/audit.tftest.hcl"); !ok {
		t.Errorf("terraform test tests/audit.tftest.hcl failed:\n%s", out)
	}
}

// The prefix is free-form in the module and existing deployments use it as they like, so the module must make a
// valid bucket name from any of them, or stop at plan with a precondition naming the prefix; never at apply.
func TestModule_LogBucketNameIsValidForAnyPrefix(t *testing.T) {
	m := moduleTestResults(t)
	for i, prefix := range bucketNamePrefixes {
		out, ok := m.file(t, fmt.Sprintf("tests/gen_bucket_name_%d.tftest.hcl", i))
		if ok {
			continue
		}
		if strings.Contains(out, "precondition failed") && !strings.Contains(out, "Test assertion failed") && strings.Contains(strings.ToLower(out), "prefix") {
			continue
		}
		t.Errorf("prefix %q (%d characters): the log bucket name is neither a valid S3 name (3 to 63 lowercase letters, digits, dots and hyphens, none of S3's reserved prefixes or suffixes) nor refused at plan by a precondition naming the prefix:\n%s", prefix, len(prefix), out)
	}
}

// enable_cloudfront_logging and enable_waf do nothing without enable_cloudfront. Silently ignoring them would let
// someone believe they had logs or a WAF, so the plan warns (a check block: a warning, not an error, so no existing
// plan breaks).
func TestModule_WarnsWhenAFeatureNeedsCloudFront(t *testing.T) {
	m := moduleTestResults(t)
	for _, c := range featureCases {
		out, ok := m.file(t, "tests/gen_"+c.name+".tftest.hcl")
		// terraform test fails a run on a failed check block, where plan and apply only warn; any other error is real.
		warned := strings.Contains(out, "Error: Check block assertion failed")
		other := false
		for _, e := range regexp.MustCompile(`(?m)^Error: (.*)$`).FindAllStringSubmatch(out, -1) {
			if e[1] != "Check block assertion failed" {
				other = true
			}
		}
		switch {
		case other:
			t.Errorf("%s: the plan fails with an error other than a check block's, so it would stop a real plan:\n%s", c.name, out)
		case c.warn && !warned:
			t.Errorf("%s (enable_cloudfront %v, enable_cloudfront_logging %v, enable_waf %v): no check block warns that the feature needs enable_cloudfront:\n%s",
				c.name, c.cloudfront, c.logging, c.waf, out)
		case !c.warn && (warned || !ok):
			t.Errorf("%s (enable_cloudfront %v, enable_cloudfront_logging %v, enable_waf %v): a check block warns, or the plan fails, though nothing is wrong:\n%s",
				c.name, c.cloudfront, c.logging, c.waf, out)
		}
	}
}

// make tf-check runs the module tests; make test, which make deploy runs, must stay offline and quick.
func TestMake_TfCheckRunsTheModuleTests(t *testing.T) {
	src, err := os.ReadFile("../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	makefile := string(src)
	_, recipe := makeRule(t, makefile, "tf-check")
	runs := false
	for _, line := range recipe {
		if strings.Contains(line, terraformTestsEnv+"=1") && strings.Contains(line, "go test") &&
			strings.Contains(line, "./deployments") && regexp.MustCompile(`-run[ =]'?\^?TestModule`).MatchString(line) {
			runs = true
		}
	}
	if !runs {
		t.Errorf("make tf-check does not run %s=1 go test ./deployments -run TestModule; recipe:\n%s", terraformTestsEnv, strings.Join(recipe, "\n"))
	}
	_, testRecipe := makeRule(t, makefile, "test")
	for _, line := range testRecipe {
		if strings.Contains(line, terraformTestsEnv) {
			t.Errorf("make test sets %s (%q); make deploy runs make test, which must not need the network", terraformTestsEnv, line)
		}
	}
	for _, line := range strings.Split(makefile, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(line, "\t") {
			continue
		}
		if strings.Contains(line, terraformTestsEnv) {
			t.Errorf("the Makefile sets %s outside a recipe (%q), so every target, make test included, would run the module tests", terraformTestsEnv, line)
		}
	}
}

// The same, statically, for machines without terraform.
func TestModule_HasACheckForFeaturesWithoutCloudFront(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	var asserts string
	for _, c := range blocks(module, "check") {
		for _, a := range nested(c, "assert") {
			if cond, ok := a.Body.Attributes["condition"]; ok {
				asserts += expandLocals(t, module, sourceOf(t, module, cond.Expr)) + "\n"
			}
		}
	}
	for _, feature := range []string{"enable_cloudfront_logging", "enable_waf"} {
		if !refersToVar(asserts, feature) || !refersToVar(asserts, "enable_cloudfront") {
			t.Errorf("no check block asserts on var.%s together with var.enable_cloudfront", feature)
		}
	}
}

// Every resource configuring the log bucket is created where the bucket is, and nothing makes it public, deletable
// with its logs, or ACL-based.
func TestCloudFrontLogging_BucketCompanionsInUSEast1AndPrivate(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	bucket, _ := cloudFrontLogBucket(t, module)

	all := blocks(module, "resource", "aws_s3_bucket", bucket)
	for _, typ := range []string{"aws_s3_bucket_policy", "aws_s3_bucket_public_access_block", "aws_s3_bucket_lifecycle_configuration",
		"aws_s3_bucket_server_side_encryption_configuration", "aws_s3_bucket_versioning", "aws_s3_bucket_ownership_controls"} {
		all = append(all, bucketCompanions(t, module, typ, bucket)...)
	}
	for _, r := range all {
		if a, ok := r.Body.Attributes["provider"]; !ok || strings.TrimSpace(sourceOf(t, module, a.Expr)) != "aws.us_east_1" {
			t.Errorf("%s does not use provider aws.us_east_1, where the log bucket and its deliveries are", label(r))
		}
	}

	for _, b := range blocks(module, "resource", "aws_s3_bucket") {
		if a, ok := b.Body.Attributes["force_destroy"]; ok && strings.TrimSpace(sourceOf(t, module, a.Expr)) != "false" {
			t.Errorf("%s sets force_destroy; an apply or destroy could delete the access logs with it", label(b))
		}
	}
	for _, acl := range blocks(module, "resource", "aws_s3_bucket_acl") {
		t.Errorf("%s: the log bucket keeps ACLs disabled; access goes through the bucket policy", label(acl))
	}
	for _, oc := range blocks(module, "resource", "aws_s3_bucket_ownership_controls") {
		for _, rule := range nested(oc, "rule") {
			if own, _ := literal(rule.Body.Attributes["object_ownership"]); own != "BucketOwnerEnforced" {
				t.Errorf("%s sets object_ownership %q; only BucketOwnerEnforced keeps ACLs disabled", label(oc), own)
			}
		}
	}
	for _, s := range bucketCompanions(t, module, "aws_s3_bucket_server_side_encryption_configuration", bucket) {
		for _, rule := range nested(s, "rule") {
			for _, def := range nested(rule, "apply_server_side_encryption_by_default") {
				if alg, _ := literal(def.Body.Attributes["sse_algorithm"]); alg != "AES256" && alg != "aws:kms" {
					t.Errorf("%s sse_algorithm = %q, want AES256 or aws:kms", label(s), alg)
				}
			}
		}
	}
}

// A count written with `||` creates a feature's resources with CloudFront off (or the feature off).
func TestGates_NeedBothSwitches(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	for _, f := range module {
		for _, b := range f.body.Blocks {
			if len(b.Labels) < 2 || (b.Type != "resource" && b.Type != "data") {
				continue
			}
			g := gate(t, module, b)
			if (refersToVar(g, "enable_cloudfront_logging") || refersToVar(g, "enable_waf")) && strings.Contains(g, "||") {
				t.Errorf("%s: count/for_each %q uses ||; it must need enable_cloudfront and the feature's switch both", label(b), strings.Join(strings.Fields(g), " "))
			}
		}
	}
}

// Exactly the two reviewed rules, each as reviewed: an override or a scope-down would quietly narrow what they block.
func TestWAF_ExactlyTheTwoReviewedRules(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	acls := blocks(module, "resource", "aws_wafv2_web_acl")
	if len(acls) != 1 {
		t.Fatalf("found %d aws_wafv2_web_acl resources, want exactly 1", len(acls))
	}
	acl := acls[0]
	for _, dyn := range nested(acl, "dynamic") {
		t.Errorf("%s has a dynamic %v block; its rules must be the two written out", label(acl), dyn.Labels)
	}
	rules := nested(acl, "rule")
	if len(rules) != 2 {
		t.Errorf("%s has %d rules, want exactly 2 (IP reputation and rate limit)", label(acl), len(rules))
	}
	var reputation, rate int
	for _, r := range rules {
		name, _ := literal(r.Body.Attributes["name"])
		if m := statementOf(r, "managed_rule_group_statement"); m != nil {
			reputation++
			if n, _ := literal(m.Body.Attributes["name"]); n != "AWSManagedRulesAmazonIpReputationList" {
				t.Errorf("rule %q uses managed rule group %q, want AWSManagedRulesAmazonIpReputationList", name, n)
			}
			if len(nested(m, "rule_action_override")) > 0 {
				t.Errorf("rule %q overrides a rule's action in the IP reputation group", name)
			}
			if len(nested(m, "scope_down_statement")) > 0 {
				t.Errorf("rule %q narrows the IP reputation group with a scope_down_statement", name)
			}
		}
		if s := statementOf(r, "rate_based_statement"); s != nil {
			rate++
			if k, _ := literal(s.Body.Attributes["aggregate_key_type"]); k != "IP" {
				t.Errorf("rule %q aggregate_key_type = %q, want IP", name, k)
			}
			if len(nested(s, "scope_down_statement")) > 0 {
				t.Errorf("rule %q limits only part of the traffic with a scope_down_statement", name)
			}
		}
	}
	if reputation != 1 || rate != 1 {
		t.Errorf("%s has %d IP reputation and %d rate-based rules, want 1 of each", label(acl), reputation, rate)
	}
}

func TestDeployDocs_TerraformAuditReview(t *testing.T) {
	raw, err := os.ReadFile("../docs/deploy.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	paras := strings.Split(doc, "\n\n")

	// The limit counts per address, and an IPv6 client has a whole prefix of addresses to rotate through.
	ipv6 := false
	for _, p := range paras {
		if strings.Contains(p, "waf_rate_limit") && regexp.MustCompile(`(?i)per (IP|address)|each IP address|one IP address`).MatchString(p) && strings.Contains(p, "IPv6") {
			ipv6 = true
		}
	}
	if !ipv6 {
		t.Error("docs/deploy.md does not say, where it describes waf_rate_limit, that the limit is per IP address and that an IPv6 client can rotate addresses within its prefix")
	}

	// Published prices for vended log delivery differ between AWS's pages, so a flat per-GB charge is not stated as fact.
	for _, p := range paras {
		if regexp.MustCompile(`(?i)per GB`).MatchString(p) && !regexp.MustCompile(`(?i)\b(differ\w*|var(y|ies)|disagree\w*)\b`).MatchString(p) {
			t.Errorf("docs/deploy.md states a per-GB cost as fact, without saying sources differ:\n%s", strings.TrimSpace(p))
		}
	}

	// The deploying role's permissions are worked out by hand, not from a policy simulator.
	if !strings.Contains(doc, "`logs:UpdateDeliveryConfiguration`") {
		t.Error("docs/deploy.md's logging permissions do not include `logs:UpdateDeliveryConfiguration`")
	}
	for _, p := range paras {
		if (strings.Contains(p, "logs:PutDeliverySource") || strings.Contains(p, "wafv2:CreateWebACL")) && !regexp.MustCompile(`(?i)approximat`).MatchString(p) {
			t.Errorf("docs/deploy.md lists the deploying role's permissions without saying the list is approximate:\n%s", strings.TrimSpace(p))
		}
	}

	// A lock file with hashes for three platforms gains a fourth the first time init runs anywhere else.
	other := false
	for _, p := range paras {
		if strings.Contains(p, "init") && strings.Contains(p, lockFile) && regexp.MustCompile(`(?i)\bhash`).MatchString(p) &&
			regexp.MustCompile(`(?i)\b(another|other|different|outside)\b[^.]{0,60}platform`).MatchString(p) {
			other = true
		}
	}
	if !other {
		t.Error("docs/deploy.md does not say that terraform init on a platform outside the three locked ones adds its hash to .terraform.lock.hcl, and to refresh it with the documented command")
	}
}

// ACLs stay disabled because the module says so, not because S3's default for new buckets happens to agree; an
// account or organisation setting, or a later default, cannot then turn them on.
func TestCloudFrontLogging_OwnershipIsExplicitlyBucketOwnerEnforced(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	bucket, _ := cloudFrontLogBucket(t, module)
	controls := bucketCompanions(t, module, "aws_s3_bucket_ownership_controls", bucket)
	if len(controls) != 1 {
		t.Fatalf("found %d aws_s3_bucket_ownership_controls on aws_s3_bucket.%s, want exactly 1 setting BucketOwnerEnforced", len(controls), bucket)
	}
	oc := controls[0]
	owned := false
	for _, rule := range nested(oc, "rule") {
		if own, _ := literal(rule.Body.Attributes["object_ownership"]); own == "BucketOwnerEnforced" {
			owned = true
		}
	}
	if !owned {
		t.Errorf("%s does not set object_ownership = \"BucketOwnerEnforced\"", label(oc))
	}
	if a, ok := oc.Body.Attributes["provider"]; !ok || strings.TrimSpace(sourceOf(t, module, a.Expr)) != "aws.us_east_1" {
		t.Errorf("%s does not use provider aws.us_east_1, where the log bucket is", label(oc))
	}
	b := blocks(module, "resource", "aws_s3_bucket", bucket)[0]
	if g, want := strings.Join(strings.Fields(gate(t, module, oc)), " "), strings.Join(strings.Fields(gate(t, module, b)), " "); g != want {
		t.Errorf("%s is gated by %q, the bucket by %q; they must exist together", label(oc), g, want)
	}
}

func TestDeployDocs_TerraformAuditReviewRound2(t *testing.T) {
	read := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	files := map[string]string{
		"docs/deploy.md": read("../docs/deploy.md"),
		"deployments/environments/example/README.md":              read(filepath.Join(exampleDir, "README.md")),
		"deployments/terraform_modules/rudolph_web/waf.tf":        read(filepath.Join(moduleRoot, "waf.tf")),
		"deployments/terraform_modules/rudolph_web/_variables.tf": read(filepath.Join(moduleRoot, "_variables.tf")),
		"deployments/environments/example/_variables.tf":          read(filepath.Join(exampleDir, "_variables.tf")),
	}

	// AWSManagedRulesAmazonIpReputationList ships AWSManagedIPDDoSList with action count: those addresses are
	// counted, not blocked, unless overridden.
	for path, src := range files {
		// Markdown here is a line per paragraph or table row; Terraform comments are hard-wrapped, so a comment is
		// read with the lines around it.
		sep := "\n"
		if strings.HasSuffix(path, ".tf") {
			sep = "\n\n"
		}
		for _, part := range strings.Split(src, sep) {
			if regexp.MustCompile(`(?i)\bboth\s+(are\s+)?block`).MatchString(part) {
				t.Errorf("%s says both rules block; the IP reputation group's AWSManagedIPDDoSList only counts:\n%s", path, strings.TrimSpace(part))
			}
			if strings.Contains(part, "DDoS") && !(strings.Contains(part, "AWSManagedIPDDoSList") && regexp.MustCompile(`(?i)\bcount`).MatchString(part)) {
				t.Errorf("%s mentions DDoS without saying AWSManagedIPDDoSList only counts, so it reads as if those addresses were blocked:\n%s", path, strings.TrimSpace(part))
			}
		}
	}
	if !strings.Contains(files["docs/deploy.md"], "AWSManagedIPDDoSList") {
		t.Error("docs/deploy.md does not say that the IP reputation group's AWSManagedIPDDoSList rule counts rather than blocks")
	}

	// WAF prices change; a figure is a list price on the day it was written.
	for _, path := range []string{"docs/deploy.md", "deployments/environments/example/README.md"} {
		sep := "\n\n"
		if strings.HasSuffix(path, "README.md") {
			sep = "\n"
		}
		for _, p := range strings.Split(files[path], sep) {
			if !regexp.MustCompile(`(?i)\b(WAF|web ACL|enable_waf)\b`).MatchString(p) || !regexp.MustCompile(`(?i)US dollars|\$\s?[0-9]`).MatchString(p) {
				continue
			}
			hedged := regexp.MustCompile(`(?i)list price`).MatchString(p) && regexp.MustCompile(`(?i)pricing page`).MatchString(p)
			if path != "docs/deploy.md" {
				hedged = regexp.MustCompile(`(?i)list price|pricing page`).MatchString(p)
			}
			if !hedged {
				t.Errorf("%s gives WAF prices as fact, without saying they are list prices when written and to check the AWS WAF pricing page:\n%s", path, strings.TrimSpace(p))
			}
		}
	}

	// Copying the example's lock file over an environment that already locks a newer 6.x release downgrades the provider.
	var upgrade string
	for heading, text := range markdownSections(files["docs/deploy.md"]) {
		if strings.HasPrefix(heading, "Upgrading: ") {
			upgrade += text
		}
	}
	copies := 0
	for _, line := range strings.Split(upgrade, "\n") {
		if !strings.Contains(line, "cp ") || !strings.Contains(line, lockFile) {
			continue
		}
		copies++
		if !regexp.MustCompile(`(?i)downgrad`).MatchString(line) || !regexp.MustCompile(`(?i)\bnewer\b`).MatchString(line) {
			t.Errorf("the upgrade step copying the example's %s does not warn that it can downgrade the provider when the environment already locks a newer release, or say to copy only if not newer:\n%s", lockFile, strings.TrimSpace(line))
		}
	}
	if copies == 0 {
		t.Errorf("no \"Upgrading: ...\" section of docs/deploy.md has the step copying the example's %s", lockFile)
	}
}
