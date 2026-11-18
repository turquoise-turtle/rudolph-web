# Behavioural checks for the Terraform audit (2026-11-19), run by deployments/terraform_module_test.go with
# `terraform test`. Mock providers mean nothing here talks to AWS; the static checks in deployments/*_test.go
# cannot tell `&&` from `||` in a count, or what web_acl_id and the bucket policy come out as.

mock_provider "aws" {
  # The provider validates these as JSON or ARNs even when mocked, and a mock's default is a random string.
  mock_data "aws_iam_policy_document" {
    defaults = {
      json = "{\"Version\":\"2012-10-17\",\"Statement\":[]}"
    }
  }
  mock_resource "aws_iam_role" {
    defaults = {
      arn = "arn:aws:iam::123456789012:role/audit_rudolph_web"
    }
  }
  mock_resource "aws_cloudfront_distribution" {
    defaults = {
      arn = "arn:aws:cloudfront::123456789012:distribution/EAUDIT"
    }
  }
}

mock_provider "aws" {
  alias = "us_east_1"

  mock_resource "aws_s3_bucket" {
    defaults = {
      arn = "arn:aws:s3:::audit-cloudfront-logs"
    }
  }
  mock_resource "aws_cloudwatch_log_delivery_source" {
    defaults = {
      arn = "arn:aws:logs:us-east-1:123456789012:delivery-source:audit"
    }
  }
  mock_resource "aws_cloudwatch_log_delivery_destination" {
    defaults = {
      arn = "arn:aws:logs:us-east-1:123456789012:delivery-destination:audit"
    }
  }
  mock_resource "aws_wafv2_web_acl" {
    defaults = {
      arn = "arn:aws:wafv2:us-east-1:123456789012:global/webacl/audit/00000000-0000-0000-0000-000000000000"
    }
  }
}

variables {
  prefix              = "audit"
  region              = "ap-southeast-2"
  aws_account_id      = "123456789012"
  tenant_id           = "00000000-0000-0000-0000-000000000000"
  client_id           = "00000000-0000-0000-0000-000000000001"
  dynamodb_table_name = "audit_rudolph_store"

  # Only hashed (filebase64sha256); any file in the module will do, and a mock never uploads it.
  lambda_zip = "main.tf"
}

# Each feature exists only when both CloudFront and its own switch are on.
run "nothing_without_cloudfront" {
  command = apply

  variables {
    enable_cloudfront         = false
    enable_cloudfront_logging = true
    enable_waf                = true
  }

  assert {
    condition     = length(aws_s3_bucket.cloudfront_logs) == 0 && length(aws_s3_bucket_policy.cloudfront_logs) == 0 && length(aws_s3_bucket_public_access_block.cloudfront_logs) == 0 && length(aws_s3_bucket_lifecycle_configuration.cloudfront_logs) == 0 && length(aws_s3_bucket_server_side_encryption_configuration.cloudfront_logs) == 0
    error_message = "the log bucket or one of its companions is created with enable_cloudfront off"
  }
  assert {
    condition     = length(aws_cloudwatch_log_delivery_source.cloudfront) == 0 && length(aws_cloudwatch_log_delivery_destination.cloudfront) == 0 && length(aws_cloudwatch_log_delivery.cloudfront) == 0
    error_message = "a log delivery is created with enable_cloudfront off"
  }
  assert {
    condition     = length(aws_wafv2_web_acl.web) == 0
    error_message = "the web ACL is created with enable_cloudfront off"
  }
}

run "cloudfront_alone" {
  command = apply

  variables {
    enable_cloudfront         = true
    enable_cloudfront_logging = false
    enable_waf                = false
  }

  assert {
    condition     = length(aws_s3_bucket.cloudfront_logs) == 0 && length(aws_cloudwatch_log_delivery.cloudfront) == 0
    error_message = "logging resources are created with enable_cloudfront_logging off"
  }
  assert {
    condition     = length(aws_wafv2_web_acl.web) == 0
    error_message = "the web ACL is created with enable_waf off"
  }
  assert {
    condition     = aws_cloudfront_distribution.web[0].web_acl_id == null
    error_message = "web_acl_id is not null with enable_waf off"
  }
}

run "logging_without_waf" {
  command = apply

  variables {
    enable_cloudfront         = true
    enable_cloudfront_logging = true
    enable_waf                = false
  }

  assert {
    condition     = length(aws_s3_bucket.cloudfront_logs) == 1 && length(aws_s3_bucket_policy.cloudfront_logs) == 1 && length(aws_s3_bucket_public_access_block.cloudfront_logs) == 1 && length(aws_s3_bucket_lifecycle_configuration.cloudfront_logs) == 1 && length(aws_s3_bucket_server_side_encryption_configuration.cloudfront_logs) == 1
    error_message = "the log bucket and its four companions are not all created once with CloudFront and logging on"
  }
  assert {
    condition     = length(aws_cloudwatch_log_delivery.cloudfront) == 1
    error_message = "no log delivery with CloudFront and logging on"
  }
  assert {
    condition     = length(aws_wafv2_web_acl.web) == 0 && aws_cloudfront_distribution.web[0].web_acl_id == null
    error_message = "the web ACL is created or attached with enable_waf off"
  }
}

run "waf_without_logging" {
  command = apply

  variables {
    enable_cloudfront         = true
    enable_cloudfront_logging = false
    enable_waf                = true
  }

  assert {
    condition     = length(aws_wafv2_web_acl.web) == 1
    error_message = "no web ACL with CloudFront and the WAF on"
  }
  assert {
    condition     = aws_cloudfront_distribution.web[0].web_acl_id == aws_wafv2_web_acl.web[0].arn
    error_message = "web_acl_id is not the web ACL's ARN"
  }
  assert {
    condition     = length(aws_s3_bucket.cloudfront_logs) == 0 && length(aws_cloudwatch_log_delivery.cloudfront) == 0
    error_message = "logging resources are created with enable_cloudfront_logging off"
  }
}

# The delivery service may write only for this account and this deployment's delivery source, so no other
# account's or distribution's logs can be delivered here, and nothing reaches the bucket without TLS.
run "log_bucket_policy" {
  command = apply

  variables {
    enable_cloudfront         = true
    enable_cloudfront_logging = true
  }

  assert {
    condition = alltrue([
      for s in jsondecode(aws_s3_bucket_policy.cloudfront_logs[0].policy).Statement :
      try(s.Principal.Service, "") == "delivery.logs.amazonaws.com" &&
      try(s.Condition.StringEquals["aws:SourceAccount"], "") == var.aws_account_id &&
      contains([
        try(s.Condition.StringEquals["aws:SourceArn"], ""),
        try(s.Condition.ArnEquals["aws:SourceArn"], ""),
        try(s.Condition.ArnLike["aws:SourceArn"], ""),
      ], aws_cloudwatch_log_delivery_source.cloudfront[0].arn)
      if s.Effect == "Allow"
    ])
    error_message = "an Allow statement in the log bucket policy is not limited to delivery.logs.amazonaws.com with aws:SourceAccount = the account and aws:SourceArn = this deployment's delivery source"
  }
  assert {
    condition = length([
      for s in jsondecode(aws_s3_bucket_policy.cloudfront_logs[0].policy).Statement : s
      if s.Effect == "Allow"
    ]) > 0
    error_message = "the log bucket policy admits no log delivery at all"
  }
  assert {
    condition = anytrue([
      for s in jsondecode(aws_s3_bucket_policy.cloudfront_logs[0].policy).Statement :
      s.Effect == "Deny" &&
      contains(flatten([s.Action]), "s3:*") &&
      contains(flatten([s.Resource]), aws_s3_bucket.cloudfront_logs[0].arn) &&
      contains(flatten([s.Resource]), "${aws_s3_bucket.cloudfront_logs[0].arn}/*") &&
      try(s.Condition.Bool["aws:SecureTransport"], "") == "false"
    ])
    error_message = "the log bucket policy does not deny s3:* without TLS on both the bucket and its objects"
  }
}

# AWS WAF accepts rate limits from 10 to 2,000,000,000; outside that, plan must refuse rather than apply fail.
run "waf_rate_limit_too_low" {
  command = plan

  variables {
    enable_cloudfront = true
    enable_waf        = true
    waf_rate_limit    = 9
  }

  expect_failures = [var.waf_rate_limit]
}

run "waf_rate_limit_too_high" {
  command = plan

  variables {
    enable_cloudfront = true
    enable_waf        = true
    waf_rate_limit    = 2000000001
  }

  expect_failures = [var.waf_rate_limit]
}

run "waf_rate_limit_lowest" {
  command = plan

  variables {
    enable_cloudfront = true
    enable_waf        = true
    waf_rate_limit    = 10
  }
}

run "waf_rate_limit_default" {
  command = plan

  variables {
    enable_cloudfront = true
    enable_waf        = true
    waf_rate_limit    = 1000
  }
}
