PKG = github.com/turquoise-turtle/rudolph-web
RUDOLPH_WEB_DEPLOYMENT_ZIP_PATH = $(PWD)/build/package/rudolph_web_deployment.zip
TERRAFORM_DEPLOYMENTS_DIR = $(PWD)/deployments/environments
TF_DEFAULT_FLAGS = --var lambda_zip="$(RUDOLPH_WEB_DEPLOYMENT_ZIP_PATH)"

# Check to ensure the environment is being passed in as an arg like `ENV=<YOUR_ENVIRONMENT>`
# or is set using environment variables like `export ENV=<YOUR_ENVIRONMENT>`
#
.check-args:
ifndef ENV
	$(error ENV is not set. please use `ENV=<YOUR_ENV> make deploy` or `export ENV=<YOUR_ENV>.`)
endif

TERRAFORM_DEPLOYMENTS_ENV_DIR = $(TERRAFORM_DEPLOYMENTS_DIR)/$(ENV)

# Default target for `make`; builds the Lambda and applies the Terraform.
#
#
deploy: .check-args test build tf-init justdeploy

# Applies without running tests or rebuilding. Useful right after a build, but prefer `make deploy`.
justdeploy: .check-args
	terraform -chdir=$(TERRAFORM_DEPLOYMENTS_ENV_DIR) apply $(TF_DEFAULT_FLAGS)

# Equivalent of `terraform plan` in the desired environment directory.
#
#
plan: .check-args
	terraform -chdir=$(TERRAFORM_DEPLOYMENTS_ENV_DIR) plan $(TF_DEFAULT_FLAGS)

# Equivalent to a `terraform init` in the desired environment directory.
# Required upon first deployment and when the tf state drifts too far from the local cached state.
#
tf-init: .check-args
	terraform -chdir=$(TERRAFORM_DEPLOYMENTS_ENV_DIR) init

# Destroys the current deployment. The SSM parameters holding the secrets are not managed by
# Terraform by default, so they survive; delete them yourself if you mean to.
#
destroy: tf-init
	terraform -chdir=$(TERRAFORM_DEPLOYMENTS_ENV_DIR) destroy $(TF_DEFAULT_FLAGS)

# Formats and statically checks the Terraform without touching any AWS account.
#
#
tf-check:
	terraform fmt -recursive -check deployments
	terraform -chdir=deployments/environments/example init -backend=false
	terraform -chdir=deployments/environments/example validate

# Install dependencies
#
#
deps:
	go mod download
	go mod tidy

# Checks the Go dependencies: known vulnerabilities that affect the code (fails if any), then every module in the
# build graph that has a newer version or is deprecated (for information; it does not fail).
#
GOVULNCHECK_VERSION = v1.9.0
deps-check:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...
	go list -m -u -f '{{if or .Update .Deprecated}}{{.Path}} {{.Version}}{{with .Update}} (newer: {{.Version}}){{end}}{{with .Deprecated}} (deprecated: {{.}}){{end}}{{end}}' \
		$$(go list -deps -test -tags integration -f '{{with .Module}}{{if not .Main}}{{.Path}}{{end}}{{end}}' ./... | sort -u)
# The listing covers only modules that provide a package the build or the tests compile. The rest of the module graph
# (modules that other modules' go.mod files merely mention) is never built, and go mod tidy drops any pin of it.

# Compiles the Lambda binary and packages it for provided.al2023 on arm64.
#
#
build: deps
	@sh -c "'$(CURDIR)/scripts/build.sh'"

# Run all golang unit tests, including the static checks on the Terraform.
#
#
test:
	go vet ./...
	go test ./...

# Initializes the environment directory and files
#
#
init-env: .check-args
	@sh -c "'$(CURDIR)/scripts/new_env.sh' $(ENV)"

#
#
#
.PHONY: .check-args deploy justdeploy plan tf-init destroy tf-check deps deps-check build test init-env
