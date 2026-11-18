package deployments

// Terraform audit, review round 1 (2026-11-19). The static checks in terraform_audit_test.go read the
// configuration as text, which cannot show what a plan does: that a count written with `||` creates the log bucket
// without CloudFront, that web_acl_id comes out as the ACL's ARN, or that "${var.prefix}-..." is not a valid S3
// bucket name for a prefix such as my_org, which plans cleanly and only fails at apply. These tests run
// `terraform test` against the module with mock providers, so nothing talks to AWS, and skip when terraform is not
// installed.
//
// The first run downloads hashicorp/aws (about 700 MB) into a plugin cache under the user's cache directory;
// later runs reuse it.

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

const moduleRoot = moduleDir + "/rudolph_web"

// initModuleCopy copies the module, its tests and the example's lock file into a temporary directory and runs
// terraform init there, so no .terraform directory or lock file is left in the checkout.
func initModuleCopy(t *testing.T) (dir string, terraform string) {
	t.Helper()
	terraform, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("terraform is not installed; the module's terraform test files were not run")
	}
	dir = t.TempDir()
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
		t.Fatal(err)
	}
	// The example's lock file, where there is one, so the tests use the provider build deploys use.
	if b, err := os.ReadFile(filepath.Join(exampleDir, lockFile)); err == nil {
		if err := os.WriteFile(filepath.Join(dir, lockFile), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := terraformCmd(t, terraform, dir, "init", "-backend=false", "-input=false", "-no-color").CombinedOutput(); err != nil {
		t.Fatalf("terraform init in a copy of the module: %v\n%s", err, out)
	}
	return dir, terraform
}

func terraformCmd(t *testing.T, terraform, dir string, args ...string) *exec.Cmd {
	t.Helper()
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

// runTerraformTest writes a test file into the module copy and runs only that file.
func runTerraformTest(t *testing.T, terraform, dir, name, src string) (string, bool) {
	t.Helper()
	rel := filepath.Join("tests", name+".tftest.hcl")
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(filepath.Join(dir, rel))
	out, err := terraformCmd(t, terraform, dir, "test", "-no-color", "-filter="+rel).CombinedOutput()
	return string(out), err == nil
}

// mockProviders returns the committed test file's mock_provider blocks, so generated test files mock exactly as it does.
func mockProviders(t *testing.T) string {
	t.Helper()
	path := filepath.Join(moduleRoot, "tests", "audit.tftest.hcl")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, diags := hclparse.NewParser().ParseHCL(src, path)
	if diags.HasErrors() {
		t.Fatalf("parse %s: %s", path, diags.Error())
	}
	var out string
	for _, b := range f.Body.(*hclsyntax.Body).Blocks {
		if b.Type == "mock_provider" {
			out += string(b.Range().SliceBytes(src)) + "\n\n"
		}
	}
	if out == "" {
		t.Fatalf("%s has no mock_provider blocks", path)
	}
	return out
}

func moduleVariables(prefix string, cloudfront, logging, waf bool) string {
	b := func(v bool) string {
		if v {
			return "true"
		}
		return "false"
	}
	return `
variables {
  prefix                    = "` + prefix + `"
  region                    = "ap-southeast-2"
  aws_account_id            = "123456789012"
  tenant_id                 = "00000000-0000-0000-0000-000000000000"
  client_id                 = "00000000-0000-0000-0000-000000000001"
  dynamodb_table_name       = "audit_rudolph_store"
  lambda_zip                = "main.tf"
  enable_cloudfront         = ` + b(cloudfront) + `
  enable_cloudfront_logging = ` + b(logging) + `
  enable_waf                = ` + b(waf) + `
}
`
}

// The switch combinations, web_acl_id, the bucket policy and the rate limit's validation, from the committed test file.
func TestModule_TerraformTestFile(t *testing.T) {
	if _, err := os.Stat(filepath.Join(moduleRoot, "tests", "audit.tftest.hcl")); err != nil {
		t.Fatal(err)
	}
	dir, terraform := initModuleCopy(t)
	out, err := terraformCmd(t, terraform, dir, "test", "-no-color", "-filter=tests/audit.tftest.hcl").CombinedOutput()
	if err != nil {
		t.Errorf("terraform test tests/audit.tftest.hcl failed: %v\n%s", err, out)
	}
}

var validBucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// The prefix is free-form in the module and existing deployments use it as they like, so the module must make a
// valid bucket name from any of them, or stop at plan with a precondition naming the prefix; never at apply.
func TestModule_LogBucketNameIsValidForAnyPrefix(t *testing.T) {
	dir, terraform := initModuleCopy(t)
	for _, prefix := range []string{"my_org", "Prod", strings.Repeat("abcdefghij", 4)} {
		src := mockProviders(t) + moduleVariables(prefix, true, true, false) + `
run "bucket_name" {
  command = apply

  assert {
    condition     = length(aws_s3_bucket.cloudfront_logs) == 1 && can(regex("^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$", aws_s3_bucket.cloudfront_logs[0].bucket)) && !strcontains(aws_s3_bucket.cloudfront_logs[0].bucket, "..")
    error_message = "invalid S3 bucket name: ${try(aws_s3_bucket.cloudfront_logs[0].bucket, "(none)")}"
  }
}
`
		out, ok := runTerraformTest(t, terraform, dir, "bucket_name", src)
		if ok {
			continue
		}
		if strings.Contains(out, "precondition failed") && !strings.Contains(out, "Test assertion failed") && strings.Contains(strings.ToLower(out), "prefix") {
			continue
		}
		t.Errorf("prefix %q (%d characters): the log bucket name is neither valid (lowercase letters, digits, dots and hyphens, 3 to 63 characters) nor refused at plan by a precondition naming the prefix:\n%s", prefix, len(prefix), out)
	}
}

// enable_cloudfront_logging and enable_waf do nothing without enable_cloudfront. Silently ignoring them would let
// someone believe they had logs or a WAF, so the plan warns (a check block: a warning, not an error, so no existing
// plan breaks).
func TestModule_WarnsWhenAFeatureNeedsCloudFront(t *testing.T) {
	dir, terraform := initModuleCopy(t)
	cases := []struct {
		name                     string
		cloudfront, logging, waf bool
		warn                     bool
	}{
		{"logging_without_cloudfront", false, true, false, true},
		{"waf_without_cloudfront", false, false, true, true},
		{"both_with_cloudfront", true, true, true, false},
		{"nothing_on", false, false, false, false},
	}
	for _, c := range cases {
		src := mockProviders(t) + moduleVariables("audit", c.cloudfront, c.logging, c.waf) + `
run "plan" {
  command = plan
}
`
		out, ok := runTerraformTest(t, terraform, dir, c.name, src)
		// terraform test fails a run on a failed check block, where plan and apply only warn; any other error is real.
		warned := strings.Contains(out, "Error: Check block assertion failed")
		otherError := regexp.MustCompile(`(?m)^Error: (.*)$`).FindAllStringSubmatch(out, -1)
		other := false
		for _, e := range otherError {
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
