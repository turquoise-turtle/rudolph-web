// Package deployments holds static checks on the Terraform configuration. `go test` would otherwise
// never look at it, and a `terraform validate` says nothing about least privilege, secret handling or
// whether the CloudFront distribution in front of the Function URL forwards what the app needs.
//
// The checks parse HCL; none of them talks to AWS.
package deployments

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

const (
	moduleDir  = "terraform_modules"
	exampleDir = "environments/example"

	// The one cacheable route: internal/web serves the embedded stylesheet under /static/.
	staticPathPattern = "/static/*"
)

type tfFile struct {
	path string
	body *hclsyntax.Body
	src  []byte
}

func loadTerraform(t *testing.T, root string) []tfFile {
	t.Helper()
	var files []tfFile
	parser := hclparse.NewParser()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && info.Name() == ".terraform" {
			return filepath.SkipDir
		}
		if info.IsDir() || !strings.HasSuffix(path, ".tf") {
			return nil
		}
		f, diags := parser.ParseHCLFile(path)
		if diags.HasErrors() {
			t.Fatalf("parse %s: %s", path, diags.Error())
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		files = append(files, tfFile{path: path, body: f.Body.(*hclsyntax.Body), src: src})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no Terraform files under %s", root)
	}
	return files
}

func blocks(files []tfFile, typ string, labels ...string) []*hclsyntax.Block {
	var out []*hclsyntax.Block
	for _, f := range files {
		for _, b := range f.body.Blocks {
			if b.Type != typ || len(b.Labels) < len(labels) {
				continue
			}
			match := true
			for i, l := range labels {
				if b.Labels[i] != l {
					match = false
				}
			}
			if match {
				out = append(out, b)
			}
		}
	}
	return out
}

func literal(attr *hclsyntax.Attribute) (string, bool) {
	if attr == nil {
		return "", false
	}
	v, diags := attr.Expr.Value(nil)
	if diags.HasErrors() || !v.IsKnown() || v.IsNull() {
		return "", false
	}
	if v.Type().FriendlyName() != "string" {
		return "", false
	}
	return v.AsString(), true
}

func nested(b *hclsyntax.Block, typ string) []*hclsyntax.Block {
	var out []*hclsyntax.Block
	for _, nb := range b.Body.Blocks {
		if nb.Type == typ {
			out = append(out, nb)
		}
	}
	return out
}

// sourceOf returns the configuration text behind an expression, for the attributes that are
// interpolations rather than literals.
func sourceOf(t *testing.T, files []tfFile, expr hclsyntax.Expression) string {
	t.Helper()
	rng := expr.Range()
	for _, f := range files {
		if f.path == rng.Filename {
			return string(rng.SliceBytes(f.src))
		}
	}
	t.Fatalf("source for %s not found", rng)
	return ""
}

// bodySource returns the configuration text of a whole block.
func bodySource(t *testing.T, files []tfFile, b *hclsyntax.Block) string {
	t.Helper()
	rng := b.Body.SrcRange
	for _, f := range files {
		if f.path == rng.Filename {
			return string(rng.SliceBytes(f.src))
		}
	}
	t.Fatalf("source for %s not found", rng)
	return ""
}

// An environment must deploy the module in this checkout, not whatever is on a remote default branch.
func TestExampleEnvironmentUsesLocalModule(t *testing.T) {
	files := loadTerraform(t, exampleDir)
	mods := blocks(files, "module")
	if len(mods) == 0 {
		t.Fatalf("%s instantiates no module", exampleDir)
	}
	for _, m := range mods {
		src, ok := literal(m.Body.Attributes["source"])
		if !ok {
			t.Fatalf("module %v has a non-literal source", m.Labels)
		}
		if !strings.HasPrefix(src, "./") && !strings.HasPrefix(src, "../") {
			t.Errorf("module %q sources %q; the environment must use the local module", m.Labels[0], src)
		}
		if _, err := os.Stat(filepath.Join(exampleDir, src)); err != nil {
			t.Errorf("module %q sources %q, which does not exist: %v", m.Labels[0], src, err)
		}
	}
}

// Provider constraints that cannot be satisfied together make `terraform init` fail.
func TestAWSProviderConstraintsAreConsistent(t *testing.T) {
	constraints := map[string][]string{}
	for _, f := range loadTerraform(t, ".") {
		for _, tb := range f.body.Blocks {
			if tb.Type != "terraform" {
				continue
			}
			for _, rp := range nested(tb, "required_providers") {
				aws, ok := rp.Body.Attributes["aws"]
				if !ok {
					continue
				}
				obj, ok := aws.Expr.(*hclsyntax.ObjectConsExpr)
				if !ok {
					continue
				}
				for _, item := range obj.Items {
					key, diags := item.KeyExpr.Value(nil)
					if diags.HasErrors() || key.AsString() != "version" {
						continue
					}
					val, diags := item.ValueExpr.Value(nil)
					if diags.HasErrors() {
						continue
					}
					constraints[val.AsString()] = append(constraints[val.AsString()], f.path)
				}
			}
		}
	}
	if len(constraints) == 0 {
		t.Fatal("no hashicorp/aws version constraint found")
	}
	if len(constraints) > 1 {
		var parts []string
		for c, files := range constraints {
			parts = append(parts, c+" in "+strings.Join(files, ", "))
		}
		sort.Strings(parts)
		t.Errorf("conflicting hashicorp/aws version constraints:\n  %s", strings.Join(parts, "\n  "))
	}
}

// Lambda creates a log group itself, without retention, so logs would be kept forever.
func TestLambdaLogGroupHasRetention(t *testing.T) {
	files := loadTerraform(t, moduleDir)
	fns := blocks(files, "resource", "aws_lambda_function")
	if len(fns) == 0 {
		t.Fatal("the module defines no Lambda function")
	}
	groups := blocks(files, "resource", "aws_cloudwatch_log_group")
	if len(groups) == 0 {
		t.Fatal("the module defines a Lambda function but no log group")
	}
	for _, g := range groups {
		attr, ok := g.Body.Attributes["retention_in_days"]
		if !ok {
			t.Errorf("log group %q does not set retention_in_days", g.Labels[1])
			continue
		}
		if !strings.Contains(sourceOf(t, files, attr.Expr), "var.") {
			t.Errorf("log group %q hard-codes its retention; it should come from a variable", g.Labels[1])
		}
	}
	// The function must depend on the group, or Lambda races Terraform and creates one without retention.
	for _, fn := range fns {
		src := bodySource(t, files, fn)
		if !strings.Contains(src, "aws_cloudwatch_log_group") {
			t.Errorf("Lambda %q does not depend on its log group; Lambda may create one without retention", fn.Labels[1])
		}
	}
}

// The binary is built for provided.al2023 on arm64 by scripts/build.sh; a mismatch fails at the first invocation.
func TestLambdaRuntimeMatchesTheBuild(t *testing.T) {
	files := loadTerraform(t, moduleDir)
	for _, fn := range blocks(files, "resource", "aws_lambda_function") {
		if runtime, _ := literal(fn.Body.Attributes["runtime"]); runtime != "provided.al2023" {
			t.Errorf("Lambda %q runtime = %q, want provided.al2023", fn.Labels[1], runtime)
		}
		if handler, _ := literal(fn.Body.Attributes["handler"]); handler != "bootstrap" {
			t.Errorf("Lambda %q handler = %q, want bootstrap", fn.Labels[1], handler)
		}
		arch := fn.Body.Attributes["architectures"]
		if arch == nil || !strings.Contains(sourceOf(t, files, arch.Expr), "arm64") {
			t.Errorf("Lambda %q is not built for arm64", fn.Labels[1])
		}
	}
}

// policyStatements returns every statement block of every aws_iam_policy_document, with its source text.
func policyStatements(t *testing.T, files []tfFile) []*hclsyntax.Block {
	t.Helper()
	var out []*hclsyntax.Block
	for _, doc := range blocks(files, "data", "aws_iam_policy_document") {
		out = append(out, nested(doc, "statement")...)
		for _, dyn := range nested(doc, "dynamic") {
			for _, content := range nested(dyn, "content") {
				out = append(out, content)
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no IAM policy statements found")
	}
	return out
}

var wildcardOnly = regexp.MustCompile(`(^|[\[\s,])"\*"`)

// Least privilege: the role reads two parameters, a KMS key, one DynamoDB table and writes its own logs.
func TestNoWildcardResourcesInPolicies(t *testing.T) {
	files := loadTerraform(t, moduleDir)
	for _, st := range policyStatements(t, files) {
		actions := st.Body.Attributes["actions"]
		resources := st.Body.Attributes["resources"]
		if actions == nil {
			continue
		}
		actionSrc := sourceOf(t, files, actions.Expr)
		// An assume-role trust policy has principals instead of resources.
		if len(nested(st, "principals")) > 0 && resources == nil {
			continue
		}
		if wildcardOnly.MatchString(actionSrc) || strings.Contains(actionSrc, `:*"`) {
			t.Errorf("%s: statement grants wildcard actions %s", st.Body.SrcRange.Filename, strings.Join(strings.Fields(actionSrc), " "))
		}
		if resources == nil {
			t.Errorf("%s: statement for %s has no resources", st.Body.SrcRange.Filename, strings.Join(strings.Fields(actionSrc), " "))
			continue
		}
		resourceSrc := sourceOf(t, files, resources.Expr)
		if wildcardOnly.MatchString(resourceSrc) {
			t.Errorf("%s: statement for %s allows Resource \"*\"", st.Body.SrcRange.Filename, strings.Join(strings.Fields(actionSrc), " "))
		}
	}
}

// The managed AWSLambdaBasicExecutionRole policy allows logs on every resource.
func TestNoManagedWildcardPolicyAttachments(t *testing.T) {
	files := loadTerraform(t, moduleDir)
	for _, f := range files {
		if strings.Contains(string(f.src), "arn:aws:iam::aws:policy") {
			t.Errorf("%s attaches an AWS managed policy; they are broader than this function needs", f.path)
		}
	}
}

// The app issues bounded queries only, and a Scan would defeat that.
func TestNoDynamoDBScan(t *testing.T) {
	files := loadTerraform(t, moduleDir)
	for _, st := range policyStatements(t, files) {
		actions := st.Body.Attributes["actions"]
		if actions == nil {
			continue
		}
		src := sourceOf(t, files, actions.Expr)
		for _, forbidden := range []string{"dynamodb:Scan", "dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:DeleteItem", "dynamodb:BatchWriteItem"} {
			if strings.Contains(src, forbidden) {
				t.Errorf("%s: the read-only viewer must not be granted %s", st.Body.SrcRange.Filename, forbidden)
			}
		}
	}
	// It must, however, be able to read the table and its indexes.
	var granted []string
	for _, st := range policyStatements(t, files) {
		if actions := st.Body.Attributes["actions"]; actions != nil {
			granted = append(granted, sourceOf(t, files, actions.Expr))
		}
	}
	all := strings.Join(granted, "\n")
	for _, want := range []string{"dynamodb:GetItem", "dynamodb:Query"} {
		if !strings.Contains(all, want) {
			t.Errorf("the Lambda role is never granted %s, which the data layer needs", want)
		}
	}
}

// Secrets reach the function through Parameter Store, never through Terraform.
func TestSecretsAreNeverInTheConfiguration(t *testing.T) {
	files := append(loadTerraform(t, moduleDir), loadTerraform(t, exampleDir)...)

	plainSecret := regexp.MustCompile(`RUDOLPH_WEB_(CLIENT_SECRET|COOKIE_KEY)\s*=`)
	for _, f := range files {
		if loc := plainSecret.FindString(string(f.src)); loc != "" {
			t.Errorf("%s sets %s as a Lambda environment variable; secrets must come from SSM", f.path, strings.TrimSpace(loc))
		}
	}

	params := blocks(files, "resource", "aws_ssm_parameter")
	for _, p := range params {
		if typ, _ := literal(p.Body.Attributes["type"]); typ != "SecureString" {
			t.Errorf("SSM parameter %q has type %q, want SecureString", p.Labels[1], typ)
		}
		ignored := false
		for _, lc := range nested(p, "lifecycle") {
			if attr, ok := lc.Body.Attributes["ignore_changes"]; ok && strings.Contains(sourceOf(t, files, attr.Expr), "value") {
				ignored = true
			}
		}
		if !ignored {
			t.Errorf("SSM parameter %q does not ignore_changes on value; Terraform would overwrite the real secret", p.Labels[1])
		}
		if v, ok := literal(p.Body.Attributes["value"]); ok && !strings.Contains(strings.ToLower(v), "placeholder") {
			t.Errorf("SSM parameter %q has the literal value %q; only a placeholder may appear here", p.Labels[1], v)
		}
	}

	// Nothing secret may travel through tfvars either: it is committed, and it lands in state.
	raw, err := os.ReadFile(filepath.Join(exampleDir, "config.auto.tfvars.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vars map[string]any
	if err := json.Unmarshal(raw, &vars); err != nil {
		t.Fatalf("config.auto.tfvars.json is not valid JSON: %v", err)
	}
	for k := range vars {
		l := strings.ToLower(k)
		if strings.Contains(l, "secret") || strings.Contains(l, "cookie_key") || strings.Contains(l, "password") {
			t.Errorf("config.auto.tfvars.json carries %q; secrets belong in Parameter Store, not in tfvars or state", k)
		}
	}
}

// The function is reachable through a Function URL, with the app doing its own OIDC.
func TestFunctionURLIsPublicAndUnauthenticated(t *testing.T) {
	files := loadTerraform(t, moduleDir)
	urls := blocks(files, "resource", "aws_lambda_function_url")
	if len(urls) != 1 {
		t.Fatalf("found %d aws_lambda_function_url resources, want exactly 1", len(urls))
	}
	if at, _ := literal(urls[0].Body.Attributes["authorization_type"]); at != "NONE" {
		t.Errorf("function URL authorization_type = %q; the app authenticates its own users with OIDC", at)
	}
}

// CloudFront is optional, and when it is on it must reach the Function URL and pass through everything
// the app's session, OIDC callback and cross-origin checks depend on.
func TestCloudFrontWiring(t *testing.T) {
	files := loadTerraform(t, moduleDir)
	dists := blocks(files, "resource", "aws_cloudfront_distribution")
	if len(dists) != 1 {
		t.Fatalf("found %d aws_cloudfront_distribution resources, want exactly 1", len(dists))
	}
	d := dists[0]

	count, ok := d.Body.Attributes["count"]
	if !ok {
		t.Fatal("the CloudFront distribution is not optional: it has no count")
	}
	if !strings.Contains(sourceOf(t, files, count.Expr), "var.") {
		t.Error("the CloudFront distribution's count does not come from a variable")
	}

	origins := nested(d, "origin")
	if len(origins) == 0 {
		t.Fatal("the CloudFront distribution has no origin")
	}
	originSrc := bodySource(t, files, origins[0])
	if !strings.Contains(originSrc, "aws_lambda_function_url") {
		t.Error("the CloudFront origin is not the Lambda Function URL")
	}
	if !strings.Contains(originSrc, "https-only") {
		t.Error("the CloudFront origin does not talk to the Function URL over HTTPS only")
	}

	behaviors := nested(d, "default_cache_behavior")
	if len(behaviors) != 1 {
		t.Fatalf("found %d default_cache_behavior blocks, want 1", len(behaviors))
	}
	defaultSrc := bodySource(t, files, behaviors[0])
	if !strings.Contains(defaultSrc, "redirect-to-https") && !strings.Contains(defaultSrc, "https-only") {
		t.Error("the default cache behavior allows plain HTTP to the viewer")
	}
	for _, method := range []string{"POST", "DELETE"} {
		if !strings.Contains(defaultSrc, method) {
			t.Errorf("the default cache behavior does not allow %s; logout and future admin actions need it", method)
		}
	}
	if !strings.Contains(defaultSrc, "aws_cloudfront_cache_policy.disabled") &&
		!strings.Contains(defaultSrc, "4135ea2d-6df8-44a3-9df3-4b5a84be39ad") {
		t.Error("the default cache behavior does not use the CachingDisabled policy; authenticated HTML must never be cached")
	}

	// Only the stylesheet may be cached.
	var static *hclsyntax.Block
	for _, ob := range nested(d, "ordered_cache_behavior") {
		if p, _ := literal(ob.Body.Attributes["path_pattern"]); p == staticPathPattern {
			static = ob
		}
	}
	if static == nil {
		t.Fatalf("no ordered_cache_behavior for %q; the stylesheet would be uncacheable", staticPathPattern)
	}
	staticSrc := bodySource(t, files, static)
	if strings.Contains(staticSrc, "4135ea2d-6df8-44a3-9df3-4b5a84be39ad") {
		t.Errorf("the %q behavior disables caching", staticPathPattern)
	}
	for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		if strings.Contains(staticSrc, `"`+method+`"`) {
			t.Errorf("the %q behavior allows %s on a static asset", staticPathPattern, method)
		}
	}
}

// What CloudFront forwards decides whether sessions, the OIDC callback and Go's cross-origin
// protection work at all.
func TestOriginRequestPolicyForwardsWhatTheAppNeeds(t *testing.T) {
	files := loadTerraform(t, moduleDir)
	policies := blocks(files, "resource", "aws_cloudfront_origin_request_policy")
	if len(policies) != 1 {
		t.Fatalf("found %d origin request policies, want exactly 1", len(policies))
	}
	p := policies[0]
	src := bodySource(t, files, p)

	cookies := nested(p, "cookies_config")
	if len(cookies) != 1 {
		t.Fatal("the origin request policy has no cookies_config")
	}
	if b, _ := literal(cookies[0].Body.Attributes["cookie_behavior"]); b != "all" {
		t.Errorf("cookie_behavior = %q; the session cookie must reach the origin", b)
	}
	if q := nested(p, "query_strings_config"); len(q) != 1 {
		t.Fatal("the origin request policy has no query_strings_config")
	} else if b, _ := literal(q[0].Body.Attributes["query_string_behavior"]); b != "all" {
		t.Errorf("query_string_behavior = %q; the OIDC callback and pagination cursors are query strings", b)
	}

	headers := nested(p, "headers_config")
	if len(headers) != 1 {
		t.Fatal("the origin request policy has no headers_config")
	}
	headerSrc := bodySource(t, files, headers[0])
	for _, want := range []string{"Origin", "Sec-Fetch-Site", "Sec-Fetch-Mode", "Sec-Fetch-Dest", "Sec-Fetch-User", "Authorization"} {
		if !strings.Contains(headerSrc, `"`+want+`"`) {
			t.Errorf("the origin request policy does not forward %s", want)
		}
	}
	// A Lambda Function URL rejects a request whose Host is not its own domain, so the viewer Host
	// must not be forwarded; the app is configured with the public URL instead.
	if regexp.MustCompile(`"Host"`).MatchString(headerSrc) {
		t.Error(`the origin request policy forwards "Host"; a Lambda Function URL origin rejects a foreign Host header`)
	}
	// Cookie and query strings have their own config blocks; whitelisting them as headers silently does nothing.
	if strings.Contains(headerSrc, `"Cookie"`) {
		t.Error(`"Cookie" belongs in cookies_config, not in the header whitelist`)
	}
	if !strings.Contains(src, "whitelist") && !strings.Contains(src, "allViewer") {
		t.Error("the origin request policy neither whitelists headers nor forwards all of them")
	}
}

// The public URL the app is configured with must be the one users visit, or the OIDC redirect and the
// trusted origin are wrong.
func TestRedirectURLFollowsThePublicOrigin(t *testing.T) {
	files := loadTerraform(t, moduleDir)
	fns := blocks(files, "resource", "aws_lambda_function")
	if len(fns) == 0 {
		t.Fatal("no Lambda function")
	}
	src := bodySource(t, files, fns[0])
	for _, want := range []string{
		"RUDOLPH_WEB_REDIRECT_URL",
		"RUDOLPH_WEB_TENANT_ID",
		"RUDOLPH_WEB_CLIENT_ID",
		"RUDOLPH_WEB_CLIENT_SECRET_SSM_PARAMETER",
		"RUDOLPH_WEB_COOKIE_KEY_SSM_PARAMETER",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the Lambda environment does not set %s", want)
		}
	}
	if !strings.Contains(src, "auth/callback") {
		t.Error("the redirect URL does not end in the /auth/callback path the app serves")
	}
}

// Every variable is documented, and every required one is either in the example tfvars or supplied by make.
func TestVariablesAreDocumentedAndProvided(t *testing.T) {
	suppliedByMake := map[string]bool{"lambda_zip": true}

	for _, dir := range []string{moduleDir, exampleDir} {
		for _, v := range blocks(loadTerraform(t, dir), "variable") {
			if _, ok := v.Body.Attributes["description"]; !ok {
				t.Errorf("%s: variable %q has no description", dir, v.Labels[0])
			}
		}
	}

	raw, err := os.ReadFile(filepath.Join(exampleDir, "config.auto.tfvars.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vars map[string]any
	if err := json.Unmarshal(raw, &vars); err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, v := range blocks(loadTerraform(t, exampleDir), "variable") {
		declared[v.Labels[0]] = true
		if _, hasDefault := v.Body.Attributes["default"]; hasDefault {
			continue
		}
		if _, set := vars[v.Labels[0]]; !set && !suppliedByMake[v.Labels[0]] {
			t.Errorf("variable %q is required but config.auto.tfvars.json does not set it", v.Labels[0])
		}
	}
	for k := range vars {
		if !declared[k] {
			t.Errorf("config.auto.tfvars.json sets %q, which the environment does not declare", k)
		}
	}
}
