#
# The Entra client secret and the cookie key.
#
# Neither is a Lambda environment variable: the function gets the parameter names and reads the
# values at cold start. Terraform never learns a value, so none of them reaches the repository,
# config.auto.tfvars.json or the state file.
#
# The recommended flow is to create both parameters outside Terraform (see docs/deploy.md) and leave
# create_ssm_parameters false. Setting it true creates them with a placeholder so a fresh account can
# be bootstrapped in one apply; the operator then writes the real values, and ignore_changes keeps
# Terraform from putting the placeholder back.
#

# The AWS managed key that Parameter Store uses by default. Its real ARN is needed for kms:Decrypt,
# since an alias ARN is not a valid IAM resource.
data "aws_kms_alias" "ssm" {
  count = var.ssm_kms_key_arn == "" ? 1 : 0
  name  = "alias/aws/ssm"
}

locals {
  ssm_kms_key_arn = var.ssm_kms_key_arn != "" ? var.ssm_kms_key_arn : data.aws_kms_alias.ssm[0].target_key_arn
}

resource "aws_ssm_parameter" "client_secret" {
  count = var.create_ssm_parameters ? 1 : 0

  name        = local.client_secret_parameter
  description = "Entra ID client secret for ${local.function_name}"
  type        = "SecureString"
  key_id      = var.ssm_kms_key_arn == "" ? null : var.ssm_kms_key_arn
  value       = "PLACEHOLDER-run-aws-ssm-put-parameter-with-the-real-value"
  tags        = local.tags

  lifecycle {
    ignore_changes = [value]
  }
}

resource "aws_ssm_parameter" "cookie_key" {
  count = var.create_ssm_parameters ? 1 : 0

  name        = local.cookie_key_parameter
  description = "Base64 32-byte session cookie key for ${local.function_name}"
  type        = "SecureString"
  key_id      = var.ssm_kms_key_arn == "" ? null : var.ssm_kms_key_arn
  value       = "PLACEHOLDER-run-aws-ssm-put-parameter-with-the-real-value"
  tags        = local.tags

  lifecycle {
    ignore_changes = [value]
  }
}
