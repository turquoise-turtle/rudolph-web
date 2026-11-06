#
# The function and its public URL
#

# Managed explicitly so logs expire. Lambda would otherwise create this group itself, without retention.
resource "aws_cloudwatch_log_group" "web" {
  name              = "/aws/lambda/${local.function_name}"
  retention_in_days = var.log_retention_in_days
  tags              = local.tags
}

resource "aws_lambda_function" "web" {
  function_name = local.function_name
  description   = "Read-only admin web view for Rudolph"
  role          = aws_iam_role.web.arn

  # scripts/build.sh produces a zip holding a single arm64 binary named bootstrap.
  handler       = "bootstrap"
  runtime       = "provided.al2023"
  architectures = ["arm64"]
  publish       = false

  filename         = var.lambda_zip
  source_code_hash = filebase64sha256(var.lambda_zip)

  # Pages are rendered from one bounded query, so the function stays small and fails fast.
  memory_size = var.lambda_memory_size
  timeout     = var.lambda_timeout_seconds

  environment {
    variables = {
      RUDOLPH_WEB_TENANT_ID = var.tenant_id
      RUDOLPH_WEB_CLIENT_ID = var.client_id

      # The app serves the callback at /auth/callback and trusts this origin for cross-origin
      # protection; register the same URL on the Entra app registration.
      RUDOLPH_WEB_REDIRECT_URL             = local.public_base_url == "" ? "" : "${local.public_base_url}/auth/callback"
      RUDOLPH_WEB_POST_LOGOUT_REDIRECT_URL = var.post_logout_redirect_url
      RUDOLPH_WEB_SESSION_TTL              = var.session_ttl

      # Secrets are named, never carried: the function reads them at cold start with its own role.
      RUDOLPH_WEB_CLIENT_SECRET_SSM_PARAMETER = local.client_secret_parameter
      RUDOLPH_WEB_COOKIE_KEY_SSM_PARAMETER    = local.cookie_key_parameter

      # Read by the DynamoDB data layer that replaces store.Unavailable.
      RUDOLPH_WEB_DYNAMODB_TABLE = var.dynamodb_table_name
      RUDOLPH_WEB_EVENTS_TABLE   = var.events_dynamodb_table_name

      # The ENV= the pages' copy-and-paste Rudolph CLI commands start with.
      RUDOLPH_WEB_CLI_ENV = var.cli_env
    }
  }

  tags = local.tags

  # Create the log group first so Lambda never creates one without retention.
  depends_on = [aws_cloudwatch_log_group.web]
}

# The cheapest front door: no API Gateway, no load balancer. Authorization is NONE because the app
# runs the OIDC flow itself and treats every request as anonymous until it validates the session cookie.
resource "aws_lambda_function_url" "web" {
  function_name      = aws_lambda_function.web.function_name
  authorization_type = "NONE"
}
