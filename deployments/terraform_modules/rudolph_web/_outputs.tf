output "function_url" {
  description = "The Lambda Function URL. Without CloudFront this is the address users visit, and the value base_url must be set to."
  value       = aws_lambda_function_url.web.function_url
}

output "cloudfront_domain_name" {
  description = "Domain name of the CloudFront distribution, empty when CloudFront is disabled."
  value       = local.cloudfront_enabled ? aws_cloudfront_distribution.web[0].domain_name : ""
}

output "base_url" {
  description = "The public origin the app is configured with. Empty until base_url or a custom domain is set."
  value       = local.public_base_url
}

output "redirect_url" {
  description = "The redirect URI to register on the Entra app registration."
  value       = local.public_base_url == "" ? "" : "${local.public_base_url}/auth/callback"
}

output "lambda_function_name" {
  description = "Name of the function, for logs and `aws lambda update-function-code`."
  value       = aws_lambda_function.web.function_name
}

output "lambda_role_arn" {
  description = "ARN of the function's execution role."
  value       = aws_iam_role.web.arn
}

output "log_group_name" {
  description = "CloudWatch log group holding the function's logs."
  value       = aws_cloudwatch_log_group.web.name
}

output "client_secret_ssm_parameter" {
  description = "Name of the parameter the function reads the Entra client secret from."
  value       = local.client_secret_parameter
}

output "cookie_key_ssm_parameter" {
  description = "Name of the parameter the function reads the cookie key from."
  value       = local.cookie_key_parameter
}
