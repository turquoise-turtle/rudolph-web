module "rudolph_web" {
  # Always deploy the module from this checkout. Environments live in deployments/environments/<ENV>.
  source = "../../terraform_modules/rudolph_web"

  providers = {
    aws           = aws
    aws.us_east_1 = aws.us_east_1
  }

  prefix         = var.prefix
  region         = var.region
  aws_account_id = var.aws_account_id
  lambda_zip     = var.lambda_zip

  # Entra ID. The client secret is not here: it lives in SSM Parameter Store.
  tenant_id                = var.tenant_id
  client_id                = var.client_id
  base_url                 = var.base_url
  post_logout_redirect_url = var.post_logout_redirect_url
  session_ttl              = var.session_ttl

  # The ENV= of the copy-and-paste Rudolph CLI commands on the pages.
  cli_env = var.cli_env

  # How old a top list may be before its page warns; fit it to the ranker's schedule.
  top_list_stale_after = var.top_list_stale_after

  create_ssm_parameters = var.create_ssm_parameters
  ssm_kms_key_arn       = var.ssm_kms_key_arn

  # Rudolph's data store. enable_editing (on by default) lets admins change rules and temporary modes; false keeps it read-only.
  dynamodb_table_name  = var.dynamodb_table_name
  dynamodb_kms_key_arn = var.dynamodb_kms_key_arn
  enable_editing       = var.enable_editing

  # Rudolph's execution events table, read-only. Empty leaves the events pages unavailable.
  events_dynamodb_table_name = var.events_dynamodb_table_name

  lambda_memory_size     = var.lambda_memory_size
  lambda_timeout_seconds = var.lambda_timeout_seconds
  log_retention_in_days  = var.log_retention_in_days

  # Optional CloudFront front end and custom domain.
  enable_cloudfront         = var.enable_cloudfront
  domain_name               = var.domain_name
  acm_certificate_arn       = var.acm_certificate_arn
  route53_zone_name         = var.route53_zone_name
  use_existing_route53_zone = var.use_existing_route53_zone
  cloudfront_price_class    = var.cloudfront_price_class

  # Optional monthly cost budget; alerts only, never caps spending.
  monthly_budget_usd  = var.monthly_budget_usd
  budget_alert_emails = var.budget_alert_emails

  tags = var.tags
}
