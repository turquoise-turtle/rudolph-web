# The account also holds resources that are not rudolph-web's, so the budget only counts costs
# tagged Name = rudolph-web: local.tags sets it and so does the provider default tag every
# environment sets in _backend.tf. Overriding Name through var.tags takes those resources out of
# the budget. AWS only applies the filter once the Name cost allocation tag is activated in
# Billing and Cost Management (Cost allocation tags), and only to costs from then on; until then
# the budget sees nothing.
#
# A budget alerts; it never stops or caps spending.
#
# Budgets is a global service: the provider calls its single endpoint whatever region it is
# configured for, so the default provider is used here.
resource "aws_budgets_budget" "monthly" {
  count = var.monthly_budget_usd > 0 ? 1 : 0

  name         = "${var.prefix}_rudolph_web_monthly"
  budget_type  = "COST"
  time_unit    = "MONTHLY"
  limit_amount = tostring(var.monthly_budget_usd)
  limit_unit   = "USD"

  cost_filter {
    name   = "TagKeyValue"
    values = ["user:Name$rudolph-web"]
  }

  notification {
    notification_type          = "ACTUAL"
    comparison_operator        = "GREATER_THAN"
    threshold                  = 80
    threshold_type             = "PERCENTAGE"
    subscriber_email_addresses = var.budget_alert_emails
  }

  notification {
    notification_type          = "ACTUAL"
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    subscriber_email_addresses = var.budget_alert_emails
  }

  notification {
    notification_type          = "FORECASTED"
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    subscriber_email_addresses = var.budget_alert_emails
  }

  lifecycle {
    # A precondition rather than a cross-variable validation, which needs Terraform 1.9 while the
    # module supports 1.5.
    precondition {
      condition     = length(var.budget_alert_emails) > 0
      error_message = "budget_alert_emails must list at least one address when monthly_budget_usd is above 0."
    }
  }
}
