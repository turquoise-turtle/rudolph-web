package envcheck

// Removed settings (owner decision 3 for the editable minimum Santa version). The min_santa_version Terraform variable
// is gone: the minimum is Rudolph's stored setting, changed with `rudolph fleet min-santa-version` or the form on
// /admin/config. An environment that still has it would otherwise deploy as if nothing had changed: Terraform only
// warns about a tfvars value for an undeclared variable, and a declared but unused variable is no error at all.
//
// So Check refuses an environment that still has a retired setting anywhere: declared as a variable (in any .tf file),
// passed to a module (as an argument, including in an override file), or set in a tfvars file Terraform loads. Each
// place is its own problem, naming the setting and the file, and saying where the setting moved: to Rudolph, with
// `rudolph fleet min-santa-version` or the web form on /admin/config. This holds whatever the example environment has,
// since the example no longer has the setting to compare with. A setting that merely shares a prefix
// (min_santa_version_note) is not retired.

import (
	"strings"
	"testing"
)

func requireRetired(t *testing.T, problems []string, file string) {
	t.Helper()
	requireProblem(t, problems, "min_santa_version", file, "Rudolph", "rudolph fleet min-santa-version", "/admin/config")
}

func TestCheck_RetiredMinSantaVersionAsAVariable(t *testing.T) {
	envDir, exampleDir := dirs(t, map[string]string{
		"main.tf":                 exampleMain,
		"_variables.tf":           exampleVariables + "\nvariable \"min_santa_version\" {\n  type    = string\n  default = \"2026.9\"\n}\n",
		"config.auto.tfvars.json": `{"prefix": "prod", "region": "us-west-2", "display_time_zone": "UTC"}`,
	})
	problems := check(t, envDir, exampleDir)
	if len(problems) != 1 {
		t.Errorf("want one problem, for the declaration; got:\n%s", strings.Join(problems, "\n"))
	}
	requireRetired(t, problems, "_variables.tf")
}

func TestCheck_RetiredMinSantaVersionInTfvars(t *testing.T) {
	envDir, exampleDir := dirs(t, map[string]string{
		"main.tf":                 exampleMain,
		"_variables.tf":           exampleVariables,
		"config.auto.tfvars.json": `{"prefix": "prod", "region": "us-west-2", "display_time_zone": "UTC", "min_santa_version": "2026.10"}`,
	})
	problems := check(t, envDir, exampleDir)
	requireRetired(t, problems, "config.auto.tfvars.json")
	for _, p := range problems {
		if strings.Contains(p, "Terraform would ignore it") {
			t.Errorf("the retired setting is reported as an ordinary undeclared value, without saying where it went: %s", p)
		}
	}
}

func TestCheck_RetiredMinSantaVersionPassedToTheModule(t *testing.T) {
	for _, name := range []string{"main.tf", "main_override.tf"} {
		t.Run(name, func(t *testing.T) {
			files := map[string]string{
				"main.tf":                 exampleMain,
				"_variables.tf":           exampleVariables + "\nvariable \"min_santa_version\" {\n  type = string\n}\n",
				"config.auto.tfvars.json": `{"prefix": "prod", "region": "us-west-2", "display_time_zone": "UTC", "min_santa_version": "2026.9"}`,
			}
			module := "\nmodule \"app\" {\n  min_santa_version = var.min_santa_version\n}\n"
			if name == "main.tf" {
				files["main.tf"] = strings.Replace(exampleMain, "  display_time_zone = var.display_time_zone\n",
					"  display_time_zone = var.display_time_zone\n  min_santa_version = var.min_santa_version\n", 1)
			} else {
				files[name] = module
			}
			envDir, exampleDir := dirs(t, files)
			problems := check(t, envDir, exampleDir)
			requireRetired(t, problems, name)
			requireRetired(t, problems, "_variables.tf")
			requireRetired(t, problems, "config.auto.tfvars.json")
			if len(problems) != 3 {
				t.Errorf("want three problems (module argument, declaration, value); got:\n%s", strings.Join(problems, "\n"))
			}
		})
	}
}

func TestCheck_SimilarNamesAreNotRetired(t *testing.T) {
	envDir, exampleDir := dirs(t, map[string]string{
		"main.tf":                 exampleMain,
		"_variables.tf":           exampleVariables + "\nvariable \"min_santa_version_note\" {\n  type = string\n}\n",
		"config.auto.tfvars.json": `{"prefix": "prod", "region": "us-west-2", "display_time_zone": "UTC", "min_santa_version_note": "x"}`,
	})
	if problems := check(t, envDir, exampleDir); len(problems) != 0 {
		t.Errorf("a variable that only shares a prefix with a retired one was refused:\n%s", strings.Join(problems, "\n"))
	}
}
