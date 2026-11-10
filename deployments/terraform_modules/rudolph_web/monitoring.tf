#
# CloudWatch alarms on the function, so that a broken deploy (a bad SSM parameter, a Rudolph pin update, editing
# writes refused by IAM) is noticed without someone opening the site.
#
# Three standard alarms on AWS/Lambda metrics, which are published for free; each alarm costs about 0.10 US dollars a
# month. Keep docs/deploy.md in step with this list.
#
# Every alarm notifies var.alarm_sns_topic_arn when set, both when it fires and when it recovers. Empty creates the
# alarms with no actions, visible in CloudWatch only, as Rudolph does.
#

locals {
  alarm_actions = var.alarm_sns_topic_arn == "" ? [] : [var.alarm_sns_topic_arn]
}

# Errors counts invocations that failed, including timeouts and crashes. Pages that answer 4xx or 5xx are not
# Lambda errors, so this fires only when the function itself fails.
resource "aws_cloudwatch_metric_alarm" "lambda_errors" {
  alarm_name          = "${local.function_name}_errors"
  alarm_description   = "rudolph-web's function failed (crashed or timed out); pages are failing to load."
  namespace           = "AWS/Lambda"
  metric_name         = "Errors"
  statistic           = "Sum"
  period              = 300
  evaluation_periods  = 1
  threshold           = 1
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "notBreaching"

  dimensions = {
    FunctionName = aws_lambda_function.web.function_name
  }

  alarm_actions = local.alarm_actions
  ok_actions    = local.alarm_actions
  tags          = local.tags
}

# Throttles counts invocations refused because the account or function concurrency limit was reached.
resource "aws_cloudwatch_metric_alarm" "lambda_throttles" {
  alarm_name          = "${local.function_name}_throttles"
  alarm_description   = "rudolph-web's function was throttled; some page loads were refused."
  namespace           = "AWS/Lambda"
  metric_name         = "Throttles"
  statistic           = "Sum"
  period              = 300
  evaluation_periods  = 1
  threshold           = 1
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "notBreaching"

  dimensions = {
    FunctionName = aws_lambda_function.web.function_name
  }

  alarm_actions = local.alarm_actions
  ok_actions    = local.alarm_actions
  tags          = local.tags
}

# The slowest invocation reaching 80% of the timeout warns before pages start timing out: DynamoDB slowing down, or
# a page whose query no longer fits in var.lambda_timeout_seconds.
resource "aws_cloudwatch_metric_alarm" "lambda_duration" {
  alarm_name          = "${local.function_name}_duration"
  alarm_description   = "An invocation of rudolph-web's function took at least 80% of its ${var.lambda_timeout_seconds}s timeout."
  namespace           = "AWS/Lambda"
  metric_name         = "Duration"
  statistic           = "Maximum"
  period              = 300
  evaluation_periods  = 1
  threshold           = var.lambda_timeout_seconds * 1000 * 0.8
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "notBreaching"

  dimensions = {
    FunctionName = aws_lambda_function.web.function_name
  }

  alarm_actions = local.alarm_actions
  ok_actions    = local.alarm_actions
  tags          = local.tags
}
