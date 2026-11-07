#
# IAM: read two parameters, decrypt them, read Rudolph's two tables, write its own logs. With enable_editing, also
# write Rudolph's rules, temporary client modes and audit trail. Nothing else.
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

  # The DynamoDB reader. Every list issues one bounded query, so there is no Scan here. Writes are
  # in their own statement below, which exists only with enable_editing.
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
  # TS indexes. GetItem is for the top lists, which read the pointer to the ranker's latest snapshot.
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

  # Admin writes (enable_editing), made with Rudolph's own packages, so exactly the writes of the CLI's rule add and
  # remove and its temporary client mode: PutItem, UpdateItem and DeleteItem, also inside TransactWriteItems, which
  # has no IAM action of its own. Only on the store table itself (never an index or the events table), and only to the
  # partitions those writes touch:
  #   GlobalRules, RulesFeed       global rules and the feed rows that tell Macs about each change (globalrules)
  #   MachineRules#*               machine rules (machinerules)
  #   Machine#*                    the machine config row holding a temporary client mode (machineconfiguration);
  #                                LeadingKeys cannot limit the sort key, so this also covers a Mac's other rows
  #   GroupRules#*, GroupRegistry  group rules and the rules version bumped with each (groups)
  #   EventCollection              the index of rules whose allowed executions are collected (eventcollection)
  # The audit trail (one partition per UTC day) is a statement of its own with PutItem only: the role can add entries
  # but never change or delete one. The actions are in for_each so that they exist only when editing is on; each
  # element is one statement.
  dynamic "statement" {
    for_each = var.enable_editing ? [
      {
        sid          = "WriteRudolphRules"
        actions      = ["dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:DeleteItem"]
        leading_keys = ["GlobalRules", "RulesFeed", "MachineRules#*", "Machine#*", "GroupRules#*", "GroupRegistry", "EventCollection"]
      },
      {
        sid          = "AppendRudolphAudit"
        actions      = ["dynamodb:PutItem"]
        leading_keys = ["Audit#*"]
      },
    ] : []

    content {
      sid       = statement.value.sid
      effect    = "Allow"
      actions   = statement.value.actions
      resources = [local.dynamodb_table_arn]

      condition {
        test     = "ForAllValues:StringLike"
        variable = "dynamodb:LeadingKeys"
        values   = statement.value.leading_keys
      }
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
