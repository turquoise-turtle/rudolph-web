// lambda_zip is passed in by `make deploy`; everything else comes from config.auto.tfvars.json.
variable "lambda_zip" {
  type        = string
  description = "Full path to the zip holding the arm64 bootstrap binary"
}

variable "region" {
  type        = string
  description = "AWS region, the same one the providers in _backend.tf use"

  validation {
    condition     = can(regex("^[a-z0-9\\-]+$", var.region))
    error_message = "The aws region is not valid."
  }
}

variable "aws_account_id" {
  type        = string
  description = "AWS account ID"

  validation {
    condition     = can(regex("^[0-9]{12}$", var.aws_account_id))
    error_message = "The aws_account_id should be a 12 digit number."
  }
}

variable "prefix" {
  type        = string
  description = "Unique prefix for all resource names in this AWS account"

  validation {
    condition     = can(regex("^[a-z0-9\\-]+$", var.prefix))
    error_message = "The prefix value must contain only lowercase alphanumeric characters or dashes."
  }
}

variable "tenant_id" {
  type        = string
  description = "Entra ID tenant GUID"
}

variable "client_id" {
  type        = string
  description = "Entra ID application (client) ID"
}

variable "base_url" {
  type        = string
  description = "Public origin users visit, without a trailing slash. Empty defaults to https://<domain_name> when CloudFront is enabled."
  default     = ""
}

variable "post_logout_redirect_url" {
  type        = string
  description = "Where Entra ID sends the browser after logout. Must be registered on the app registration."
  default     = ""
}

variable "session_ttl" {
  type        = string
  description = "Session cookie lifetime, a Go duration between 5m and 12h"
  default     = "1h"
}

variable "cli_env" {
  type        = string
  description = "Rudolph environment name the pages' copy-and-paste CLI commands start with (ENV=<name>). Empty leaves ENV to the operator's shell."
  default     = ""
}

variable "create_ssm_parameters" {
  type        = bool
  description = "Create the two SecureString parameters with a placeholder value instead of creating them yourself"
  default     = false
}

variable "ssm_kms_key_arn" {
  type        = string
  description = "KMS key encrypting the two parameters. Empty uses the AWS managed alias/aws/ssm key."
  default     = ""
}

variable "dynamodb_table_name" {
  type        = string
  description = "Name of Rudolph's DynamoDB table, typically <rudolph prefix>_rudolph_store"
}

variable "dynamodb_kms_key_arn" {
  type        = string
  description = "ARN of the CMK encrypting Rudolph's table, if it has one"
  default     = ""
}

variable "events_dynamodb_table_name" {
  type        = string
  description = "Name of Rudolph's execution events table, typically <rudolph prefix>_rudolph_events. Empty leaves the events pages unavailable"
  default     = ""
}

variable "log_retention_in_days" {
  type        = number
  description = "Retention of the function's CloudWatch log group, in days"
  default     = 30
}

variable "lambda_memory_size" {
  type        = number
  description = "Function memory in MB"
  default     = 256
}

variable "lambda_timeout_seconds" {
  type        = number
  description = "Function timeout in seconds"
  default     = 10
}

variable "enable_cloudfront" {
  type        = bool
  description = "Put CloudFront in front of the Function URL; required for a custom domain"
  default     = false
}

variable "domain_name" {
  type        = string
  description = "Custom domain for the distribution, for example rudolph-web.example.com"
  default     = ""
}

variable "acm_certificate_arn" {
  type        = string
  description = "Existing us-east-1 certificate for domain_name. Empty requests one."
  default     = ""
}

variable "route53_zone_name" {
  type        = string
  description = "Route 53 hosted zone holding domain_name"
  default     = ""
}

variable "use_existing_route53_zone" {
  type        = bool
  description = "Look up route53_zone_name instead of creating it"
  default     = true
}

variable "cloudfront_price_class" {
  type        = string
  description = "CloudFront price class"
  default     = "PriceClass_100"
}

variable "tags" {
  type        = map(string)
  description = "Extra tags merged into every taggable resource"
  default     = {}
}
