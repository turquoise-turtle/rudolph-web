variable "prefix" {
  type        = string
  description = "Prefix to all resource names. Unique within the AWS account."
}

variable "region" {
  type        = string
  description = "AWS region. Must match the region the provider is configured with."
}

variable "aws_account_id" {
  type        = string
  description = "AWS account ID, used to build the SSM and DynamoDB ARNs the role is allowed to use."
}

variable "lambda_zip" {
  type        = string
  description = "Full path to the zip containing the arm64 bootstrap binary. Built by scripts/build.sh and passed in by `make deploy`."
}

#
# Application configuration. Neither secret appears here: see ssm.tf.
#
variable "tenant_id" {
  type        = string
  description = "Entra ID tenant GUID (RUDOLPH_WEB_TENANT_ID)."
}

variable "client_id" {
  type        = string
  description = "Entra ID application (client) ID (RUDOLPH_WEB_CLIENT_ID)."
}

variable "base_url" {
  type        = string
  description = "Public origin users visit, without a trailing slash, for example https://rudolph-web.example.com. Leave empty on the very first apply of a Function-URL-only deployment and set it to the function_url output afterwards. With CloudFront and a custom domain it defaults to https://<domain_name>."
  default     = ""

  validation {
    condition     = var.base_url == "" || can(regex("^https://[^/]+$", var.base_url))
    error_message = "base_url must be an https origin with no path or trailing slash, e.g. https://rudolph-web.example.com."
  }
}

variable "post_logout_redirect_url" {
  type        = string
  description = "Where Entra ID sends the browser after logout (RUDOLPH_WEB_POST_LOGOUT_REDIRECT_URL). Register this exact URL on the app registration. Empty leaves the user on Entra's signed-out page."
  default     = ""
}

variable "session_ttl" {
  type        = string
  description = "Session cookie lifetime as a Go duration between 5m and 12h (RUDOLPH_WEB_SESSION_TTL). Role changes take effect at most one TTL after the next sign-in."
  default     = "1h"
}

#
# Secrets, held in SSM Parameter Store
#
variable "client_secret_ssm_parameter" {
  type        = string
  description = "Name of the SecureString parameter holding the Entra client secret. Defaults to /<prefix>/rudolph-web/client-secret."
  default     = ""

  validation {
    condition     = var.client_secret_ssm_parameter == "" || startswith(var.client_secret_ssm_parameter, "/")
    error_message = "client_secret_ssm_parameter must be an absolute parameter name starting with /."
  }
}

variable "cookie_key_ssm_parameter" {
  type        = string
  description = "Name of the SecureString parameter holding the base64 cookie key. Defaults to /<prefix>/rudolph-web/cookie-key."
  default     = ""

  validation {
    condition     = var.cookie_key_ssm_parameter == "" || startswith(var.cookie_key_ssm_parameter, "/")
    error_message = "cookie_key_ssm_parameter must be an absolute parameter name starting with /."
  }
}

variable "create_ssm_parameters" {
  type        = bool
  description = "Create the two SecureString parameters with a placeholder value, ignoring later changes to it. Leave false and create them with `aws ssm put-parameter` so no secret, not even a placeholder, ever reaches the Terraform state."
  default     = false
}

variable "ssm_kms_key_arn" {
  type        = string
  description = "KMS key encrypting the two parameters. Empty uses the AWS managed alias/aws/ssm key, whose ARN is looked up for the kms:Decrypt grant."
  default     = ""
}

#
# Rudolph's data store, for the DynamoDB reader that replaces store.Unavailable
#
variable "dynamodb_table_name" {
  type        = string
  description = "Name of Rudolph's DynamoDB table, typically <rudolph prefix>_rudolph_store. The role gets GetItem and Query on it and its indexes, and nothing else."
}

variable "dynamodb_table_arn" {
  type        = string
  description = "ARN of that table. Empty builds it from the region, account and table name."
  default     = ""
}

variable "dynamodb_kms_key_arn" {
  type        = string
  description = "ARN of the CMK encrypting the table, if it has one. Reading an encrypted table also needs kms:Decrypt. Empty grants nothing extra."
  default     = ""
}

variable "events_dynamodb_table_name" {
  type        = string
  description = "Name of Rudolph's execution events table, typically <rudolph prefix>_rudolph_events (Rudolph's events_dynamodb_table_name output). The role gets GetItem and Query on it and its indexes, and nothing else. Empty leaves the events pages unavailable (503)."
  default     = ""
}

variable "events_dynamodb_table_arn" {
  type        = string
  description = "ARN of the events table (Rudolph's events_dynamodb_table_arn output). Empty builds it from the region, account and events table name. It is encrypted with the store table's key, so dynamodb_kms_key_arn covers it."
  default     = ""
}

#
# Lambda sizing and logs
#
variable "lambda_memory_size" {
  type        = number
  description = "Function memory in MB. The app renders small HTML pages from one bounded query."
  default     = 256

  validation {
    condition     = var.lambda_memory_size >= 128 && var.lambda_memory_size <= 1024
    error_message = "lambda_memory_size must be between 128 and 1024 MB for this application."
  }
}

variable "lambda_timeout_seconds" {
  type        = number
  description = "Function timeout in seconds. A page that cannot be rendered quickly should fail rather than hang the browser."
  default     = 10

  validation {
    condition     = var.lambda_timeout_seconds >= 1 && var.lambda_timeout_seconds <= 60
    error_message = "lambda_timeout_seconds must be between 1 and 60."
  }
}

variable "log_retention_in_days" {
  type        = number
  description = "Retention of the function's CloudWatch log group, in days."
  default     = 30
}

#
# Optional CloudFront front end
#
variable "enable_cloudfront" {
  type        = bool
  description = "Put a CloudFront distribution in front of the Function URL. Needed for a custom domain; the Function URL alone is the cheapest option."
  default     = false
}

variable "domain_name" {
  type        = string
  description = "Custom domain for the distribution, for example rudolph-web.example.com. Empty uses the cloudfront.net domain and its default certificate."
  default     = ""
}

variable "acm_certificate_arn" {
  type        = string
  description = "Existing us-east-1 ACM certificate for domain_name. Empty requests one and validates it through Route 53."
  default     = ""
}

variable "route53_zone_name" {
  type        = string
  description = "Route 53 hosted zone holding domain_name, used for the alias record and certificate validation. Empty means DNS is managed elsewhere."
  default     = ""
}

variable "use_existing_route53_zone" {
  type        = bool
  description = "Look up route53_zone_name instead of creating it. Creating a zone Terraform then owns is rarely what you want."
  default     = true
}

variable "cloudfront_price_class" {
  type        = string
  description = "CloudFront price class. PriceClass_100 is the cheapest and covers North America and Europe."
  default     = "PriceClass_100"

  validation {
    condition     = contains(["PriceClass_100", "PriceClass_200", "PriceClass_All"], var.cloudfront_price_class)
    error_message = "cloudfront_price_class must be one of PriceClass_100, PriceClass_200, PriceClass_All."
  }
}

variable "tags" {
  type        = map(string)
  description = "Extra tags merged into the tags of every taggable resource."
  default     = {}
}
