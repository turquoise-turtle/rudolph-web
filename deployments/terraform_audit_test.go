package deployments

// Terraform audit (owner approved, 2026-11-19). Five findings:
//
//  1. CloudFront kept no access logs, so there was no record of who reached the site through it. Logging is opt-in
//     (enable_cloudfront_logging), into a bucket of the module's own that is private, encrypted, TLS-only and expires
//     logs after cloudfront_log_retention_days.
//  2. The example S3 backend took no state lock, so two applies at once could corrupt the state. Terraform 1.10 and
//     later lock with a lock file in the state bucket (use_lockfile).
//  3. .terraform.lock.hcl was gitignored, so each `terraform init` could pick a different hashicorp/aws build with
//     nothing to check its hashes against. The example commits one, with hashes for the platforms deploys run on,
//     and new environments copy it.
//  4. Nothing rate-limited or filtered requests before they reached the function. A WAF on CloudFront is opt-in
//     (enable_waf), with a rate-based rule and AWS's IP reputation list. The Function URL stays directly reachable,
//     so the docs must say the WAF covers CloudFront traffic only.
//  5. docs/deploy.md documents all of it, with an upgrade note.
//
// Each check pins the outcome rather than one way of writing it: a count may go through a local, and the logs may
// go through either the distribution's logging_config or CloudFront standard logging v2.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

const lockFile = ".terraform.lock.hcl"

func boolLiteral(attr *hclsyntax.Attribute) (value, ok bool) {
	if attr == nil {
		return false, false
	}
	v, diags := attr.Expr.Value(nil)
	if diags.HasErrors() || !v.IsKnown() || v.IsNull() || v.Type().FriendlyName() != "bool" {
		return false, false
	}
	return v.True(), true
}

// refersToVar matches var.<name> but not a longer variable starting with the same name, so enable_cloudfront is not
// found in var.enable_cloudfront_logging.
func refersToVar(src, name string) bool {
	return regexp.MustCompile(`var\.` + regexp.QuoteMeta(name) + `\b`).MatchString(src)
}

func refersToResource(src, typ, name string) bool {
	return regexp.MustCompile(regexp.QuoteMeta(typ) + `\.` + regexp.QuoteMeta(name) + `\b`).MatchString(src)
}

// expandLocals replaces each local.<name> with that local's own source, so a count written through a local still
// shows which variables it depends on.
func expandLocals(t *testing.T, files []tfFile, src string) string {
	t.Helper()
	locals := map[string]string{}
	for _, f := range files {
		for _, b := range f.body.Blocks {
			if b.Type != "locals" {
				continue
			}
			for name, a := range b.Body.Attributes {
				locals[name] = sourceOf(t, files, a.Expr)
			}
		}
	}
	ref := regexp.MustCompile(`local\.([A-Za-z0-9_-]+)`)
	for i := 0; i < 8; i++ {
		src = ref.ReplaceAllStringFunc(src, func(m string) string {
			if s, ok := locals[m[len("local."):]]; ok {
				return "(" + s + ")"
			}
			return m
		})
	}
	return src
}

// gate returns what decides whether a block exists at all (its count or for_each), with locals expanded; "" when
// it always exists.
func gate(t *testing.T, files []tfFile, b *hclsyntax.Block) string {
	t.Helper()
	for _, name := range []string{"count", "for_each"} {
		if a, ok := b.Body.Attributes[name]; ok {
			return expandLocals(t, files, sourceOf(t, files, a.Expr))
		}
	}
	return ""
}

func label(b *hclsyntax.Block) string {
	return b.Type + "." + strings.Join(b.Labels, ".")
}

// requireVariable checks a variable's type and default, as written.
func requireVariable(t *testing.T, files []tfFile, where, name, typ, def string) *hclsyntax.Block {
	t.Helper()
	vs := blocks(files, "variable", name)
	if len(vs) != 1 {
		t.Errorf("%s: variable %q declared %d times, want once", where, name, len(vs))
		return nil
	}
	v := vs[0]
	if a, ok := v.Body.Attributes["type"]; !ok || strings.TrimSpace(sourceOf(t, files, a.Expr)) != typ {
		t.Errorf("%s: variable %q is not of type %s", where, name, typ)
	}
	if a, ok := v.Body.Attributes["default"]; !ok {
		t.Errorf("%s: variable %q has no default, want %s so existing environments keep working", where, name, def)
	} else if got := strings.TrimSpace(sourceOf(t, files, a.Expr)); got != def {
		t.Errorf("%s: variable %q defaults to %s, want %s", where, name, got, def)
	}
	return v
}

// requirePassedThrough checks that the example declares a module variable, passes it to the module as var.<name>
// and sets it in config.auto.tfvars.json, which is what envcheck then holds existing environments to.
func requirePassedThrough(t *testing.T, name string) {
	t.Helper()
	example := loadTerraform(t, exampleDir)
	if len(blocks(example, "variable", name)) != 1 {
		t.Errorf("the example environment does not declare variable %q", name)
	}
	passed := false
	for _, m := range blocks(example, "module", "rudolph_web") {
		if a, ok := m.Body.Attributes[name]; ok && strings.TrimSpace(sourceOf(t, example, a.Expr)) == "var."+name {
			passed = true
		}
	}
	if !passed {
		t.Errorf("the example's module \"rudolph_web\" block does not pass %s = var.%s", name, name)
	}
	raw, err := os.ReadFile(filepath.Join(exampleDir, "config.auto.tfvars.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg[name]; !ok {
		t.Errorf("config.auto.tfvars.json has no %q", name)
	}
}

func distribution(t *testing.T, files []tfFile) *hclsyntax.Block {
	t.Helper()
	dists := blocks(files, "resource", "aws_cloudfront_distribution")
	if len(dists) != 1 {
		t.Fatalf("found %d aws_cloudfront_distribution resources, want exactly 1", len(dists))
	}
	return dists[0]
}

var s3BucketRef = regexp.MustCompile(`aws_s3_bucket\.([A-Za-z0-9_-]+)`)

// cloudFrontLogBucket finds the module's bucket the distribution's access logs are delivered to, through either
// a dynamic logging_config (legacy standard logging) or aws_cloudwatch_log_delivery (standard logging v2).
func cloudFrontLogBucket(t *testing.T, files []tfFile) (bucket string, legacy bool) {
	t.Helper()
	d := distribution(t, files)

	for _, lc := range nested(d, "logging_config") {
		t.Errorf("the distribution has a static logging_config at %s; it would log with enable_cloudfront_logging off, so it must be a dynamic block", lc.DefRange())
	}
	for _, dyn := range nested(d, "dynamic") {
		if len(dyn.Labels) != 1 || dyn.Labels[0] != "logging_config" {
			continue
		}
		if !refersToVar(gate(t, files, dyn), "enable_cloudfront_logging") {
			t.Error("the distribution's dynamic logging_config is not conditional on var.enable_cloudfront_logging")
		}
		for _, content := range nested(dyn, "content") {
			a, ok := content.Body.Attributes["bucket"]
			if !ok {
				continue
			}
			src := sourceOf(t, files, a.Expr)
			// CloudFront takes the bucket's domain name here, not its name or ARN.
			if !strings.Contains(src, "bucket_domain_name") {
				t.Errorf("logging_config bucket = %s; CloudFront needs the bucket's bucket_domain_name", src)
			}
			if m := s3BucketRef.FindStringSubmatch(src); m != nil {
				return m[1], true
			}
		}
	}

	// Standard logging v2: a delivery source on the distribution, a destination on the bucket, and a delivery joining them.
	var sources, destinations []string
	for _, s := range blocks(files, "resource", "aws_cloudwatch_log_delivery_source") {
		src := bodySource(t, files, s)
		logType, _ := literal(s.Body.Attributes["log_type"])
		if refersToResource(src, "aws_cloudfront_distribution", d.Labels[1]) && logType == "ACCESS_LOGS" {
			sources = append(sources, s.Labels[1])
		}
	}
	for _, dst := range blocks(files, "resource", "aws_cloudwatch_log_delivery_destination") {
		if m := s3BucketRef.FindStringSubmatch(bodySource(t, files, dst)); m != nil {
			destinations = append(destinations, dst.Labels[1]+"="+m[1])
		}
	}
	for _, del := range blocks(files, "resource", "aws_cloudwatch_log_delivery") {
		src := bodySource(t, files, del)
		for _, s := range sources {
			if !refersToResource(src, "aws_cloudwatch_log_delivery_source", s) {
				continue
			}
			for _, pair := range destinations {
				name, b, _ := strings.Cut(pair, "=")
				if refersToResource(src, "aws_cloudwatch_log_delivery_destination", name) {
					return b, false
				}
			}
		}
	}

	t.Fatal("the CloudFront distribution's access logs go nowhere: there is neither a dynamic logging_config naming one of the module's S3 buckets nor an aws_cloudwatch_log_delivery from an ACCESS_LOGS delivery source on the distribution to a destination on one")
	return "", false
}

// bucketCompanions returns the module's resources of a type that configure the given bucket.
func bucketCompanions(t *testing.T, files []tfFile, typ, bucket string) []*hclsyntax.Block {
	t.Helper()
	var out []*hclsyntax.Block
	for _, r := range blocks(files, "resource", typ) {
		if a, ok := r.Body.Attributes["bucket"]; ok && refersToResource(sourceOf(t, files, a.Expr), "aws_s3_bucket", bucket) {
			out = append(out, r)
		}
	}
	return out
}

func TestCloudFrontLogging_OptInVariables(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	example := loadTerraform(t, exampleDir)
	requireVariable(t, module, moduleDir, "enable_cloudfront_logging", "bool", "false")
	requireVariable(t, example, exampleDir, "enable_cloudfront_logging", "bool", "false")
	requireVariable(t, example, exampleDir, "cloudfront_log_retention_days", "number", "180")
	if v := requireVariable(t, module, moduleDir, "cloudfront_log_retention_days", "number", "180"); v != nil {
		valid := false
		for _, val := range nested(v, "validation") {
			if a, ok := val.Body.Attributes["condition"]; ok && regexp.MustCompile(`var\.cloudfront_log_retention_days\s*(>\s*0|>=\s*1)\b`).MatchString(sourceOf(t, module, a.Expr)) {
				valid = true
			}
		}
		if !valid {
			t.Error("cloudfront_log_retention_days has no validation requiring more than 0 days; 0 would make the lifecycle rule invalid")
		}
	}
	requirePassedThrough(t, "enable_cloudfront_logging")
	requirePassedThrough(t, "cloudfront_log_retention_days")
}

// Nothing is created, and nothing indexes into an empty resource, unless CloudFront and logging are both on.
func TestCloudFrontLogging_NothingWithoutBothSwitches(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	bucket, _ := cloudFrontLogBucket(t, module)

	var checked []*hclsyntax.Block
	for _, f := range module {
		for _, b := range f.body.Blocks {
			// The distribution names the bucket in its dynamic logging_config, whose for_each is checked above.
			if len(b.Labels) < 2 || (b.Type != "resource" && b.Type != "data") || b.Labels[0] == "aws_cloudfront_distribution" {
				continue
			}
			typ := b.Labels[0]
			if strings.HasPrefix(typ, "aws_s3_bucket") || strings.HasPrefix(typ, "aws_cloudwatch_log_delivery") ||
				refersToResource(bodySource(t, module, b), "aws_s3_bucket", bucket) {
				checked = append(checked, b)
			}
		}
	}
	for _, b := range checked {
		g := gate(t, module, b)
		if !refersToVar(g, "enable_cloudfront_logging") || !refersToVar(g, "enable_cloudfront") {
			t.Errorf("%s is not conditional on both var.enable_cloudfront and var.enable_cloudfront_logging (count/for_each: %q)", label(b), strings.Join(strings.Fields(g), " "))
		}
	}
}

// CloudFront standard logging v2 is configured in us-east-1 whatever region the rest of the deployment is in.
func TestCloudFrontLogging_V2DeliveriesUseUSEast1(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	if _, legacy := cloudFrontLogBucket(t, module); legacy {
		return
	}
	for _, typ := range []string{"aws_cloudwatch_log_delivery_source", "aws_cloudwatch_log_delivery_destination", "aws_cloudwatch_log_delivery"} {
		for _, r := range blocks(module, "resource", typ) {
			if a, ok := r.Body.Attributes["provider"]; !ok || strings.TrimSpace(sourceOf(t, module, a.Expr)) != "aws.us_east_1" {
				t.Errorf("%s does not use provider aws.us_east_1; CloudFront log deliveries can only be set up there", label(r))
			}
		}
	}
}

func TestCloudFrontLogging_BucketIsLockedDown(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	bucket, legacy := cloudFrontLogBucket(t, module)

	pabs := bucketCompanions(t, module, "aws_s3_bucket_public_access_block", bucket)
	if len(pabs) == 0 {
		t.Errorf("the log bucket aws_s3_bucket.%s has no aws_s3_bucket_public_access_block", bucket)
	}
	for _, p := range pabs {
		for _, setting := range []string{"block_public_acls", "block_public_policy", "ignore_public_acls", "restrict_public_buckets"} {
			if v, ok := boolLiteral(p.Body.Attributes[setting]); !ok || !v {
				t.Errorf("%s: %s is not true", label(p), setting)
			}
		}
	}

	sse := bucketCompanions(t, module, "aws_s3_bucket_server_side_encryption_configuration", bucket)
	if len(sse) == 0 {
		t.Errorf("the log bucket aws_s3_bucket.%s has no aws_s3_bucket_server_side_encryption_configuration", bucket)
	}
	for _, s := range sse {
		if !regexp.MustCompile(`sse_algorithm\s*=\s*\S`).MatchString(bodySource(t, module, s)) {
			t.Errorf("%s sets no sse_algorithm", label(s))
		}
	}

	policies := bucketCompanions(t, module, "aws_s3_bucket_policy", bucket)
	if len(policies) == 0 {
		t.Errorf("the log bucket aws_s3_bucket.%s has no aws_s3_bucket_policy", bucket)
	}
	for _, p := range policies {
		src := bodySource(t, module, p)
		for _, m := range regexp.MustCompile(`data\.aws_iam_policy_document\.([A-Za-z0-9_-]+)`).FindAllStringSubmatch(src, -1) {
			for _, doc := range blocks(module, "data", "aws_iam_policy_document", m[1]) {
				src += "\n" + bodySource(t, module, doc)
			}
		}
		deny := regexp.MustCompile(`(?i)effect"?\s*[=:]\s*"Deny"`).MatchString(src)
		if !deny || !strings.Contains(src, "aws:SecureTransport") || !strings.Contains(src, "false") {
			t.Errorf("%s does not deny requests where aws:SecureTransport is false; the log bucket must be TLS-only", label(p))
		}
	}

	lifecycles := bucketCompanions(t, module, "aws_s3_bucket_lifecycle_configuration", bucket)
	if len(lifecycles) == 0 {
		t.Errorf("the log bucket aws_s3_bucket.%s has no aws_s3_bucket_lifecycle_configuration; logs would be kept forever", bucket)
	}
	expires, expiresNoncurrent := false, false
	for _, l := range lifecycles {
		for _, rule := range nested(l, "rule") {
			if status, _ := literal(rule.Body.Attributes["status"]); status != "Enabled" {
				continue
			}
			for _, e := range nested(rule, "expiration") {
				if a, ok := e.Body.Attributes["days"]; ok && refersToVar(sourceOf(t, module, a.Expr), "cloudfront_log_retention_days") {
					expires = true
				}
			}
			for _, e := range nested(rule, "noncurrent_version_expiration") {
				if a, ok := e.Body.Attributes["noncurrent_days"]; ok && strings.Contains(sourceOf(t, module, a.Expr), "var.") {
					expiresNoncurrent = true
				}
			}
		}
	}
	if len(lifecycles) > 0 && !expires {
		t.Error("no enabled lifecycle rule on the log bucket expires objects after var.cloudfront_log_retention_days days")
	}
	// With versioning on, an expired log only becomes a noncurrent version, which would then be kept forever.
	for _, v := range bucketCompanions(t, module, "aws_s3_bucket_versioning", bucket) {
		for _, vc := range nested(v, "versioning_configuration") {
			if status, _ := literal(vc.Body.Attributes["status"]); status == "Enabled" && !expiresNoncurrent {
				t.Errorf("%s turns versioning on, but no enabled lifecycle rule expires noncurrent versions", label(v))
			}
		}
	}

	// Legacy standard logging writes with an ACL grant to CloudFront's log delivery account, which a bucket with
	// ACLs disabled (BucketOwnerEnforced, the S3 default) refuses. Logging v2 needs no ACLs.
	if legacy {
		ok := false
		for _, oc := range bucketCompanions(t, module, "aws_s3_bucket_ownership_controls", bucket) {
			for _, rule := range nested(oc, "rule") {
				if own, _ := literal(rule.Body.Attributes["object_ownership"]); own == "ObjectWriter" || own == "BucketOwnerPreferred" {
					ok = true
				}
			}
		}
		if !ok {
			t.Errorf("legacy logging_config needs ACLs on aws_s3_bucket.%s: no aws_s3_bucket_ownership_controls with ObjectWriter (or BucketOwnerPreferred)", bucket)
		}
	}
}

func TestStateLocking_BackendUsesLockFile(t *testing.T) {
	files := loadTerraform(t, exampleDir)
	found := false
	for _, tb := range blocks(files, "terraform") {
		for _, be := range nested(tb, "backend") {
			if len(be.Labels) != 1 || be.Labels[0] != "s3" {
				continue
			}
			found = true
			if v, ok := boolLiteral(be.Body.Attributes["use_lockfile"]); !ok || !v {
				t.Errorf("%s: the S3 backend does not set use_lockfile = true; two applies at once could both write the state", be.DefRange().Filename)
			}
		}
	}
	if !found {
		t.Fatal("the example environment has no S3 backend")
	}
	// use_lockfile needs Terraform 1.10 or later; older releases reject the setting.
	re := regexp.MustCompile(`required_version\s*=\s*">=\s*1\.(\d+)`)
	ok := false
	for _, f := range files {
		if m := re.FindStringSubmatch(string(f.src)); m != nil {
			if minor, _ := strconv.Atoi(m[1]); minor >= 10 {
				ok = true
			}
		}
	}
	if !ok {
		t.Error("the example environment does not require Terraform >= 1.10, which use_lockfile needs")
	}
}

// awsConstraint returns the example's hashicorp/aws version constraint.
func awsConstraint(t *testing.T) string {
	t.Helper()
	for _, tb := range blocks(loadTerraform(t, exampleDir), "terraform") {
		for _, rp := range nested(tb, "required_providers") {
			aws, ok := rp.Body.Attributes["aws"]
			if !ok {
				continue
			}
			v, diags := aws.Expr.Value(nil)
			if diags.HasErrors() || !v.Type().IsObjectType() || !v.Type().HasAttribute("version") {
				continue
			}
			return v.GetAttr("version").AsString()
		}
	}
	t.Fatal("the example environment has no hashicorp/aws version constraint")
	return ""
}

func TestProviderLockFile_CommittedAndPinned(t *testing.T) {
	rel := "deployments/" + exampleDir + "/" + lockFile
	if err := exec.Command("git", "-C", "..", "check-ignore", "-q", "--no-index", rel).Run(); err == nil {
		t.Errorf("%s is gitignored; the example's provider lock file must be committed", rel)
	}
	if err := exec.Command("git", "-C", "..", "ls-files", "--error-unmatch", rel).Run(); err != nil {
		t.Errorf("%s is not tracked by git", rel)
	}

	path := filepath.Join(exampleDir, lockFile)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the example environment has no provider lock file: %v", err)
	}
	f, diags := hclparse.NewParser().ParseHCLFile(path)
	if diags.HasErrors() {
		t.Fatalf("parse %s: %s", path, diags.Error())
	}
	body := f.Body.(*hclsyntax.Body)
	var aws *hclsyntax.Block
	for _, b := range body.Blocks {
		if b.Type == "provider" && len(b.Labels) == 1 && b.Labels[0] == "registry.terraform.io/hashicorp/aws" {
			aws = b
		}
	}
	if aws == nil {
		t.Fatalf("%s does not lock registry.terraform.io/hashicorp/aws", path)
	}

	constraint := awsConstraint(t)
	cm := regexp.MustCompile(`^~>\s*(\d+)\.(\d+)$`).FindStringSubmatch(constraint)
	if cm == nil {
		t.Fatalf("versions.tf constrains hashicorp/aws to %q; this test understands only ~> X.Y", constraint)
	}
	version, _ := literal(aws.Body.Attributes["version"])
	vm := regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`).FindStringSubmatch(version)
	if vm == nil {
		t.Errorf("%s locks hashicorp/aws to %q, not a release version", path, version)
	} else {
		wantMajor, _ := strconv.Atoi(cm[1])
		minMinor, _ := strconv.Atoi(cm[2])
		major, _ := strconv.Atoi(vm[1])
		minor, _ := strconv.Atoi(vm[2])
		if major != wantMajor || minor < minMinor {
			t.Errorf("%s locks hashicorp/aws %s, outside the versions.tf constraint %q", path, version, constraint)
		}
	}
	if c, _ := literal(aws.Body.Attributes["constraints"]); c != constraint {
		t.Errorf("%s records constraints %q, want %q from versions.tf; it is stale", path, c, constraint)
	}

	// An h1: hash covers one platform's package. A lock file made by a plain `terraform init` has one, and init on
	// any other platform then fails or quietly adds its own. The lock file does not say which platform a hash is
	// for, so the count is checked here and the platforms through the documented refresh command.
	h1 := map[string]bool{}
	if a, ok := aws.Body.Attributes["hashes"]; ok {
		v, diags := a.Expr.Value(nil)
		if !diags.HasErrors() && v.CanIterateElements() {
			for it := v.ElementIterator(); it.Next(); {
				_, h := it.Element()
				if s := h.AsString(); strings.HasPrefix(s, "h1:") {
					h1[s] = true
				}
			}
		}
	}
	if len(h1) < 3 {
		t.Errorf("%s has %d h1: hashes for hashicorp/aws, want at least 3 (linux_amd64, darwin_arm64, darwin_amd64)", path, len(h1))
	}
	doc, err := os.ReadFile("../docs/deploy.md")
	if err != nil {
		t.Fatal(err)
	}
	documented := false
	for _, line := range strings.Split(string(doc), "\n") {
		if strings.Contains(line, "providers lock") && strings.Contains(line, "linux_amd64") &&
			strings.Contains(line, "darwin_arm64") && strings.Contains(line, "darwin_amd64") {
			documented = true
		}
	}
	if !documented {
		t.Error("docs/deploy.md has no `terraform providers lock` command for linux_amd64, darwin_arm64 and darwin_amd64 to refresh the lock file with")
	}
}

// make init-env runs scripts/new_env.sh, so a new environment must start with the example's lock file.
func TestProviderLockFile_CopiedIntoNewEnvironments(t *testing.T) {
	root := t.TempDir()
	script, err := os.ReadFile("../scripts/new_env.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "new_env.sh"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	example := filepath.Join(root, "deployments", "environments", "example")
	if err := os.MkdirAll(example, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(exampleDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(exampleDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(example, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if out, err := exec.Command("bash", filepath.Join(root, "scripts", "new_env.sh"), "audit").CombinedOutput(); err != nil {
		t.Fatalf("new_env.sh audit: %v\n%s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(root, "deployments", "environments", "audit", lockFile))
	if err != nil {
		t.Fatalf("new_env.sh does not give a new environment the example's %s: %v", lockFile, err)
	}
	want, _ := os.ReadFile(filepath.Join(exampleDir, lockFile))
	if !bytes.Equal(got, want) {
		t.Errorf("the new environment's %s differs from the example's", lockFile)
	}
}

func TestWAF_OptInVariable(t *testing.T) {
	requireVariable(t, loadTerraform(t, moduleDir), moduleDir, "enable_waf", "bool", "false")
	requireVariable(t, loadTerraform(t, exampleDir), exampleDir, "enable_waf", "bool", "false")
	requirePassedThrough(t, "enable_waf")
}

func requireMetricsOn(t *testing.T, owner string, b *hclsyntax.Block) {
	t.Helper()
	vcs := nested(b, "visibility_config")
	if len(vcs) != 1 {
		t.Errorf("%s has %d visibility_config blocks, want 1", owner, len(vcs))
		return
	}
	for _, setting := range []string{"cloudwatch_metrics_enabled", "sampled_requests_enabled"} {
		if v, ok := boolLiteral(vcs[0].Body.Attributes[setting]); !ok || !v {
			t.Errorf("%s: visibility_config %s is not true", owner, setting)
		}
	}
}

// statementOf returns a rule's statement block of the given kind, if it has one.
func statementOf(rule *hclsyntax.Block, kind string) *hclsyntax.Block {
	for _, st := range nested(rule, "statement") {
		if s := nested(st, kind); len(s) == 1 {
			return s[0]
		}
	}
	return nil
}

func TestWAF_WebACLOnCloudFront(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	acls := blocks(module, "resource", "aws_wafv2_web_acl")
	if len(acls) != 1 {
		t.Fatalf("found %d aws_wafv2_web_acl resources, want exactly 1", len(acls))
	}
	acl := acls[0]

	if scope, _ := literal(acl.Body.Attributes["scope"]); scope != "CLOUDFRONT" {
		t.Errorf("%s scope = %q, want CLOUDFRONT", label(acl), scope)
	}
	if a, ok := acl.Body.Attributes["provider"]; !ok || strings.TrimSpace(sourceOf(t, module, a.Expr)) != "aws.us_east_1" {
		t.Errorf("%s does not use provider aws.us_east_1; a CLOUDFRONT web ACL can only be created in us-east-1", label(acl))
	}

	for _, f := range module {
		for _, b := range f.body.Blocks {
			if len(b.Labels) < 2 || (b.Type != "resource" && b.Type != "data") || !strings.HasPrefix(b.Labels[0], "aws_wafv2_") {
				continue
			}
			g := gate(t, module, b)
			if !refersToVar(g, "enable_waf") || !refersToVar(g, "enable_cloudfront") {
				t.Errorf("%s is not conditional on both var.enable_cloudfront and var.enable_waf (count/for_each: %q)", label(b), strings.Join(strings.Fields(g), " "))
			}
		}
	}

	if da := nested(acl, "default_action"); len(da) != 1 || len(nested(da[0], "allow")) != 1 {
		t.Errorf("%s does not allow by default; the app does its own sign-in, the WAF only filters", label(acl))
	}
	requireMetricsOn(t, label(acl), acl)

	rules := nested(acl, "rule")
	for _, dyn := range nested(acl, "dynamic") {
		if len(dyn.Labels) == 1 && dyn.Labels[0] == "rule" {
			rules = append(rules, nested(dyn, "content")...)
		}
	}
	var rate, reputation *hclsyntax.Block
	for _, r := range rules {
		name, _ := literal(r.Body.Attributes["name"])
		requireMetricsOn(t, label(acl)+" rule "+strconv.Quote(name), r)
		if statementOf(r, "rate_based_statement") != nil {
			rate = r
		}
		if m := statementOf(r, "managed_rule_group_statement"); m != nil {
			n, _ := literal(m.Body.Attributes["name"])
			vendor, _ := literal(m.Body.Attributes["vendor_name"])
			if n == "AWSManagedRulesAmazonIpReputationList" && vendor == "AWS" {
				reputation = r
			}
		}
	}

	if rate == nil {
		t.Errorf("%s has no rate-based rule", label(acl))
	} else {
		limit := statementOf(rate, "rate_based_statement").Body.Attributes["limit"]
		m := regexp.MustCompile(`var\.([A-Za-z0-9_]+)`).FindStringSubmatch(func() string {
			if limit == nil {
				return ""
			}
			return sourceOf(t, module, limit.Expr)
		}())
		if m == nil {
			t.Error("the rate-based rule's limit does not come from a variable")
		} else {
			if vs := blocks(module, "variable", m[1]); len(vs) != 1 {
				t.Errorf("the rate limit variable %q is declared %d times in the module", m[1], len(vs))
			} else if _, ok := vs[0].Body.Attributes["default"]; !ok {
				t.Errorf("the rate limit variable %q has no default; existing environments would need a value", m[1])
			}
			requirePassedThrough(t, m[1])
		}
		// A rule left in count mode only counts; it never blocks anyone.
		if a := nested(rate, "action"); len(a) != 1 || len(nested(a[0], "block")) != 1 {
			t.Error("the rate-based rule does not block requests over the limit")
		}
	}
	if reputation == nil {
		t.Errorf("%s has no rule using the AWS managed rule group AWSManagedRulesAmazonIpReputationList", label(acl))
	} else if o := nested(reputation, "override_action"); len(o) != 1 || len(nested(o[0], "none")) != 1 {
		// override_action count would turn every rule in the group into count only.
		t.Error("the IP reputation rule does not use override_action { none {} }, so the group's own block actions would not apply")
	}

	d := distribution(t, module)
	a, ok := d.Body.Attributes["web_acl_id"]
	if !ok {
		t.Fatal("the CloudFront distribution sets no web_acl_id; the web ACL is not attached")
	}
	src := sourceOf(t, module, a.Expr)
	if !refersToResource(src, "aws_wafv2_web_acl", acl.Labels[1]) {
		t.Errorf("web_acl_id = %s does not name aws_wafv2_web_acl.%s", src, acl.Labels[1])
	}
	// CloudFront takes a WAFv2 web ACL by ARN; its id is refused.
	if !strings.Contains(src, ".arn") && !strings.Contains(src, "[*].arn") {
		t.Errorf("web_acl_id = %s; a WAFv2 web ACL is attached by its ARN", src)
	}
	if !strings.Contains(src, "null") && !strings.Contains(src, "one(") {
		t.Errorf("web_acl_id = %s is not null with enable_waf off", src)
	}
}

// markdownSections splits a Markdown document into heading and text, the text running to the next heading.
func markdownSections(doc string) map[string]string {
	out := map[string]string{}
	heading := ""
	inFence := false
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
		}
		if !inFence && strings.HasPrefix(line, "#") {
			heading = strings.TrimSpace(strings.TrimLeft(line, "#"))
			continue
		}
		out[heading] += line + "\n"
	}
	return out
}

func TestDeployDocs_TerraformAudit(t *testing.T) {
	raw, err := os.ReadFile("../docs/deploy.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)

	terms := []string{"enable_cloudfront_logging", "cloudfront_log_retention_days", "enable_waf", "use_lockfile", lockFile}
	for _, term := range terms {
		if !strings.Contains(doc, "`"+term+"`") {
			t.Errorf("docs/deploy.md does not mention `%s`", term)
		}
	}
	// The rate limit variable is the fixer's to name; it is whatever the rate-based rule's limit reads.
	module := loadTerraform(t, moduleDir)
	for _, acl := range blocks(module, "resource", "aws_wafv2_web_acl") {
		for _, r := range nested(acl, "rule") {
			if st := statementOf(r, "rate_based_statement"); st != nil {
				if a, ok := st.Body.Attributes["limit"]; ok {
					if m := regexp.MustCompile(`var\.([A-Za-z0-9_]+)`).FindStringSubmatch(sourceOf(t, module, a.Expr)); m != nil && !strings.Contains(doc, "`"+m[1]+"`") {
						t.Errorf("docs/deploy.md does not mention the rate limit variable `%s`", m[1])
					}
				}
			}
		}
	}
	created := markdownSections(doc)["What gets created"]
	for _, typ := range []string{"aws_s3_bucket", "aws_wafv2_web_acl"} {
		if !strings.Contains(created, "`"+typ) {
			t.Errorf("docs/deploy.md's What gets created table does not list `%s`", typ)
		}
	}

	var upgrade string
	for heading, text := range markdownSections(doc) {
		if strings.HasPrefix(heading, "Upgrading: ") {
			upgrade += text
		}
	}
	for _, term := range []string{"enable_cloudfront_logging", "enable_waf", "use_lockfile", lockFile} {
		if !strings.Contains(upgrade, term) {
			t.Errorf("no \"Upgrading: ...\" section of docs/deploy.md tells existing environments about %s", term)
		}
	}

	// The Function URL answers anyone who has its address, so a WAF on CloudFront protects only traffic through CloudFront.
	plain := false
	for _, para := range strings.Split(doc, "\n\n") {
		if strings.Contains(para, "WAF") && strings.Contains(para, "Function URL") &&
			regexp.MustCompile(`(?i)\b(directly|bypass\w*|around)\b`).MatchString(para) {
			plain = true
		}
	}
	if !plain {
		t.Error("docs/deploy.md does not say plainly that the WAF covers only traffic through CloudFront, as the Function URL is directly reachable")
	}

	readme, err := os.ReadFile(filepath.Join(exampleDir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, setting := range []string{"enable_cloudfront_logging", "cloudfront_log_retention_days", "enable_waf"} {
		if !strings.Contains(string(readme), "| `"+setting+"`") {
			t.Errorf("the example environment's README has no row for `%s` in its settings table", setting)
		}
	}
}
