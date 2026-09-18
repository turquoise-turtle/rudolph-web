#
# Printed at the end of each apply. The first apply of a Function-URL-only deployment prints the URL
# you then put into base_url.
#
output "url" {
  description = "Where users reach rudolph-web"
  value       = module.rudolph_web.base_url != "" ? module.rudolph_web.base_url : module.rudolph_web.function_url
}

output "function_url" {
  description = "The Lambda Function URL"
  value       = module.rudolph_web.function_url
}

output "redirect_url" {
  description = "Redirect URI to register on the Entra app registration"
  value       = module.rudolph_web.redirect_url
}

output "client_secret_ssm_parameter" {
  description = "Parameter holding the Entra client secret"
  value       = module.rudolph_web.client_secret_ssm_parameter
}

output "cookie_key_ssm_parameter" {
  description = "Parameter holding the session cookie key"
  value       = module.rudolph_web.cookie_key_ssm_parameter
}
