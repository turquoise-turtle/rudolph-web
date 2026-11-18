#
# Optional WAF on the CloudFront distribution, with enable_waf.
#
# It only filters: the app does its own sign-in, so everything not matched is allowed. It sees only
# requests that come through CloudFront. The Function URL stays directly reachable, by design (its
# host is random, and sign-in cannot complete there because Entra ID redirects back to base_url).
#
# A CLOUDFRONT-scope web ACL can only be created in us-east-1.
#

locals {
  waf_enabled = var.enable_cloudfront && var.enable_waf
}

resource "aws_wafv2_web_acl" "web" {
  count    = local.waf_enabled ? 1 : 0
  provider = aws.us_east_1

  name        = "${var.prefix}-rudolph-web"
  description = "Rate limit and IP reputation filter in front of rudolph-web"
  scope       = "CLOUDFRONT"

  default_action {
    allow {}
  }

  # Addresses AWS has seen take part in bots, scanning and DDoS. override_action none keeps the group's
  # own block actions; count would only record matches.
  rule {
    name     = "aws-ip-reputation"
    priority = 0

    override_action {
      none {}
    }

    statement {
      managed_rule_group_statement {
        name        = "AWSManagedRulesAmazonIpReputationList"
        vendor_name = "AWS"
      }
    }

    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "${var.prefix}-rudolph-web-ip-reputation"
      sampled_requests_enabled   = true
    }
  }

  rule {
    name     = "rate-limit-per-ip"
    priority = 1

    action {
      block {}
    }

    statement {
      rate_based_statement {
        limit                 = var.waf_rate_limit
        evaluation_window_sec = 300
        aggregate_key_type    = "IP"
      }
    }

    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "${var.prefix}-rudolph-web-rate-limit"
      sampled_requests_enabled   = true
    }
  }

  visibility_config {
    cloudwatch_metrics_enabled = true
    metric_name                = "${var.prefix}-rudolph-web"
    sampled_requests_enabled   = true
  }

  tags = local.tags
}
