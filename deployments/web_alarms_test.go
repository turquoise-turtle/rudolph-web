package deployments

// rudolph-web has alarms of its own (review before fleet rollout): without them a broken deploy (a bad SSM parameter,
// a Rudolph pin update, editing writes refused by IAM) was noticed only by someone opening the site. The function's
// Errors, Throttles and Duration are alarmed, notifying alarm_sns_topic_arn when it is set (empty: alarms with no
// actions, as in Rudolph), and the example passes the variable through so environments can set it.

import (
	"strings"
	"testing"
)

func TestWebAlarms_FunctionErrorsThrottlesAndDuration(t *testing.T) {
	module := loadTerraform(t, moduleDir)
	alarms := blocks(module, "resource", "aws_cloudwatch_metric_alarm")
	var all string
	for _, a := range alarms {
		all += bodySource(t, module, a) + "\n"
	}
	for _, metric := range []string{`"Errors"`, `"Throttles"`, `"Duration"`} {
		if !strings.Contains(all, metric) {
			t.Errorf("no alarm on the function's %s metric", metric)
		}
	}
	if !strings.Contains(all, `"AWS/Lambda"`) || !strings.Contains(all, "aws_lambda_function.web.function_name") {
		t.Errorf("the alarms do not watch aws_lambda_function.web in AWS/Lambda:\n%s", all)
	}
	for _, a := range alarms {
		src := bodySource(t, module, a)
		if !strings.Contains(src, "alarm_actions") || !strings.Contains(src, "ok_actions") {
			t.Errorf("alarm %v does not notify on alarm and on recovery", a.Labels)
		}
	}

	vs := blocks(module, "variable", "alarm_sns_topic_arn")
	if len(vs) != 1 {
		t.Fatalf("variable alarm_sns_topic_arn declared %d times", len(vs))
	}
	if d, ok := vs[0].Body.Attributes["default"]; !ok || strings.TrimSpace(sourceOf(t, module, d.Expr)) != `""` {
		t.Error(`alarm_sns_topic_arn should default to "" (no notifications), so existing environments keep working`)
	}
	var refs string
	for _, f := range module {
		refs += string(f.src)
	}
	if !strings.Contains(refs, "var.alarm_sns_topic_arn") {
		t.Error("nothing uses var.alarm_sns_topic_arn")
	}

	example := loadTerraform(t, exampleDir)
	var ex string
	for _, f := range example {
		ex += string(f.src)
	}
	if !strings.Contains(ex, "alarm_sns_topic_arn") {
		t.Error("the example environment does not pass alarm_sns_topic_arn to the module")
	}
}
