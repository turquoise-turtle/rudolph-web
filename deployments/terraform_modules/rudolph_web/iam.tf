#
# IAM: read two parameters, decrypt them, read Rudolph's two tables, write its own logs. Nothing else.
#

resource "aws_iam_role" "web" {
  name               = "${local.function_name}_role"
  path               = "/rudolph-web/"
  assume_role_policy = data.aws_iam_policy_document.assume_role.json
  tags               = local.tags
}

data "aws_iam_policy_document" "assume_role" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

# The AWS managed AWSLambdaBasicExecutionRole allows logs:* on every log group in the account.
# This function only needs to write to its own.
data "aws_iam_policy_document" "web" {
  statement {
    sid    = "WriteOwnLogs"
    effect = "Allow"
    actions = [
      "logs:CreateLogStream",
      "logs:PutLogEvents",
    ]
    resources = ["${aws_cloudwatch_log_group.web.arn}:*"]
  }

  statement {
    sid     = "ReadSecrets"
    effect  = "Allow"
    actions = ["ssm:GetParameter"]
    resources = [
      "arn:aws:ssm:${var.region}:${var.aws_account_id}:parameter${local.client_secret_parameter}",
      "arn:aws:ssm:${var.region}:${var.aws_account_id}:parameter${local.cookie_key_parameter}",
    ]
  }

  # SecureString values come back encrypted without this, and the condition keeps the grant usable
  # only through Parameter Store.
  statement {
    sid       = "DecryptSecrets"
    effect    = "Allow"
    actions   = ["kms:Decrypt"]
    resources = [local.ssm_kms_key_arn]

    condition {
      test     = "StringEquals"
      variable = "kms:ViaService"
      values   = ["ssm.${var.region}.amazonaws.com"]
    }
  }

  # Ready for the DynamoDB reader. Every list issues one bounded query, so there is no Scan here,
  # and the view is read-only, so there is no write action either.
  statement {
    sid    = "ReadRudolphStore"
    effect = "Allow"
    actions = [
      "dynamodb:GetItem",
      "dynamodb:Query",
    ]
    resources = [
      local.dynamodb_table_arn,
      "${local.dynamodb_table_arn}/index/*",
    ]
  }

  # Execution events: one bounded Query per page on the table (a Mac's partition) or one of its
  # TS indexes. GetItem is for the top lists, which read single day counters.
  dynamic "statement" {
    for_each = local.events_enabled ? [local.events_dynamodb_table_arn] : []

    content {
      sid    = "ReadRudolphEvents"
      effect = "Allow"
      actions = [
        "dynamodb:GetItem",
        "dynamodb:Query",
      ]
      resources = [
        statement.value,
        "${statement.value}/index/*",
      ]
    }
  }

  dynamic "statement" {
    for_each = var.dynamodb_kms_key_arn != "" ? [var.dynamodb_kms_key_arn] : []

    content {
      sid       = "DecryptRudolphStore"
      effect    = "Allow"
      actions   = ["kms:Decrypt"]
      resources = [statement.value]

      condition {
        test     = "StringEquals"
        variable = "kms:ViaService"
        values   = ["dynamodb.${var.region}.amazonaws.com"]
      }
    }
  }
}

resource "aws_iam_role_policy" "web" {
  name   = "${local.function_name}_policy"
  role   = aws_iam_role.web.id
  policy = data.aws_iam_policy_document.web.json
}
