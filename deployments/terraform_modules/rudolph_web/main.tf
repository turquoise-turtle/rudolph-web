#
# rudolph-web: one arm64 Lambda behind a Function URL, optionally fronted by CloudFront.
#
# The application authenticates its own users against Entra ID, so the Function URL needs no AWS
# authorization. Everything it is allowed to touch is in iam.tf; its two secrets live in Parameter
# Store, see ssm.tf.
#

locals {
  function_name = "${var.prefix}_rudolph_web"

  # Parameter names default to a per-deployment path so two environments never share a secret.
  client_secret_parameter = var.client_secret_ssm_parameter != "" ? var.client_secret_ssm_parameter : "/${var.prefix}/rudolph-web/client-secret"
  cookie_key_parameter    = var.cookie_key_ssm_parameter != "" ? var.cookie_key_ssm_parameter : "/${var.prefix}/rudolph-web/cookie-key"

  cloudfront_enabled = var.enable_cloudfront
  custom_domain      = var.enable_cloudfront && var.domain_name != ""
  route53_enabled    = var.route53_zone_name != ""
  create_certificate = local.custom_domain && var.acm_certificate_arn == ""

  # The origin users actually visit. The app is configured with it, because behind CloudFront the
  # Lambda sees its own Function URL host: the redirect URL's origin is what it trusts for
  # cross-origin protection, and what Entra ID redirects back to.
  public_base_url = var.base_url != "" ? var.base_url : (local.custom_domain ? "https://${var.domain_name}" : "")

  dynamodb_table_arn = var.dynamodb_table_arn != "" ? var.dynamodb_table_arn : "arn:aws:dynamodb:${var.region}:${var.aws_account_id}:table/${var.dynamodb_table_name}"

  events_enabled            = var.events_dynamodb_table_name != ""
  events_dynamodb_table_arn = "arn:aws:dynamodb:${var.region}:${var.aws_account_id}:table/${var.events_dynamodb_table_name}"

  tags = merge({
    Name        = "rudolph-web"
    Application = "rudolph-web"
    Prefix      = var.prefix
  }, var.tags)
}

# enable_cloudfront_logging and enable_waf only act on the CloudFront distribution. Without it they create
# nothing, so warn rather than let someone believe they have access logs or a WAF. A warning, not an error, so
# no existing plan stops.
check "features_need_cloudfront" {
  assert {
    condition     = var.enable_cloudfront || !(var.enable_cloudfront_logging || var.enable_waf)
    error_message = "enable_cloudfront_logging and enable_waf do nothing without enable_cloudfront: no access logs are kept and no WAF is attached. Turn enable_cloudfront on, or turn them off."
  }
}
