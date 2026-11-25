#
# IAM: read two parameters, decrypt them, read Rudolph's two tables, write its own logs. With enable_editing, also
# write Rudolph's rules, device groups, a Mac's and the global configuration (including clean sync requests and
# temporary client modes), the fleet job's holiday mode and the audit trail; and, with fleet_function_name as well,
# start Rudolph's fleet job. Nothing else.
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

# The AWS managed AWSLambdaBasicExecutionRole allows logs:CreateLogGroup, logs:CreateLogStream and logs:PutLogEvents on
# every log group in the account.
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

  # The DynamoDB reader. Every list issues one bounded query, so there is no Scan here; a page of a
  # group's members also reads its Macs' sensor data with one BatchGetItem. Writes are
  # in their own statement below, which exists only with enable_editing. /admin/audit reads the
  # Audit#<day> partitions through this statement with editing off too, so keep it unconditional
  # and not limited by partition.
  statement {
    sid    = "ReadRudolphStore"
    effect = "Allow"
    actions = [
      "dynamodb:BatchGetItem",
      "dynamodb:GetItem",
      "dynamodb:Query",
    ]
    resources = [
      local.dynamodb_table_arn,
      "${local.dynamodb_table_arn}/index/*",
    ]
  }

  # Execution and file access events: bounded Queries on the table (a Mac's partition) or one of
  # its TS indexes, at most five per page when an index page is refilled past the other list's rows. GetItem is for the top lists, which read the pointer to the ranker's latest snapshot.
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

  # Admin writes (enable_editing), made with Rudolph's own packages, so exactly the writes of the CLI's rule add,
  # remove and update (collecting allowed executions), its config update and temporary client mode, its group
  # create, update, delete, add and remove, and its fleet holiday: PutItem, UpdateItem and DeleteItem, also inside TransactWriteItems, which
  # has no IAM action of its own, and BatchWriteItem, which has one and which groups.DeleteGroup uses to delete a
  # group's rules (group delete --force). Only on the store table itself (never an index or the events table), and
  # only to the partitions those writes touch:
  #   GlobalRules, RulesFeed       global rules and the feed rows that tell Macs about each change (globalrules)
  #   MachineRules#*               machine rules (machinerules)
  #   Machine#*                    a Mac's config row, with its settings, clean sync request and temporary client mode
  #                                (machineconfiguration), and its Groups row of assigned groups (groups); LeadingKeys
  #                                cannot limit the sort key, so this also covers a Mac's other rows
  #   GlobalConfig                 the global config row (machineconfiguration), exactly, not as a prefix: the settings
  #                                every Mac without its own gets, including the fleet's client mode
  #   GroupRules#*, GroupRegistry  group rules and the rules version bumped with each, the registry of groups, and the
  #                                deletion of a deleted group's rules (groups)
  #   GroupMembers#*               a group's member rows, written in the same transaction as a Mac's Groups row when a
  #                                group is assigned or removed (groups)
  #   EventCollection              the index of rules whose allowed executions are collected (eventcollection)
  #   FleetHealth                  the fleet job's stale threshold and its holiday mode (fleethealth), exactly, not as a
  #                                prefix, and the web view's own marker of the last "Run the fleet job now"
  #                                (FleetHealth / RunRequested, one conditional UpdateItem); the job's own rows are in
  #                                the events table, which this role only reads
  # The audit trail (one partition per UTC day) is a statement of its own with PutItem only, so no UpdateItem or
  # DeleteItem; but a PutItem can replace an entry with the same key, and what prevents that is Rudolph's conditional put
  # (attribute_not_exists(PK)), not IAM. The actions are in for_each so that they exist only when editing is on; each
  # element is one statement, and extra_conditions adds conditions to that statement alone.
  #
  # Every ForAllValues condition on LeadingKeys has a Null check beside it: ForAllValues is true for a request that
  # carries no leading key at all, and the Null check refuses such a request.
  dynamic "statement" {
    for_each = var.enable_editing ? [
      {
        sid              = "WriteRudolphRules"
        actions          = ["dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:DeleteItem", "dynamodb:BatchWriteItem"]
        leading_keys     = ["GlobalRules", "RulesFeed", "MachineRules#*", "Machine#*", "GlobalConfig", "GroupRules#*", "GroupRegistry", "GroupMembers#*", "EventCollection", "FleetHealth"]
        extra_conditions = []
      },
      {
        sid          = "AppendRudolphAudit"
        actions      = ["dynamodb:PutItem"]
        leading_keys = ["Audit#*"]
        # Rudolph's audit entries never carry ExpiresAfter (pkg/model/audit), and the store table's TTL deletes items
        # whose ExpiresAfter has passed, so a PutItem replacing an entry with a past ExpiresAfter would get it deleted.
        # For PutItem, dynamodb:Attributes holds the item's top-level attribute names; with ForAllValues a request
        # without the key passes, so this cannot refuse a genuine audit write. Not yet confirmed against real AWS:
        # check that an audit write still succeeds and a PutItem with ExpiresAfter on Audit#<day> is denied.
        extra_conditions = [
          {
            test     = "ForAllValues:StringNotEquals"
            variable = "dynamodb:Attributes"
            values   = ["ExpiresAfter"]
          },
        ]
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

      condition {
        test     = "Null"
        variable = "dynamodb:LeadingKeys"
        values   = ["false"]
      }

      dynamic "condition" {
        for_each = statement.value.extra_conditions

        content {
          test     = condition.value.test
          variable = condition.value.variable
          values   = condition.value.values
        }
      }
    }
  }

  # "Run the fleet job now" (enable_editing and fleet_function_name): one asynchronous Invoke of Rudolph's fleet
  # Lambda, the function its schedule invokes once a day. Only that function and only unqualified, as the app invokes
  # it: no wildcard, and no version or alias suffix, which would allow every published version. The function takes no
  # input, so all an invocation can do is rebuild the fleet lists once more; the app allows one every 15 minutes. The
  # function is in the same account, so the role's policy is enough and Rudolph's function needs no resource policy.
  dynamic "statement" {
    for_each = var.enable_editing && var.fleet_function_name != "" ? ["arn:aws:lambda:${var.region}:${var.aws_account_id}:function:${var.fleet_function_name}"] : []

    content {
      sid       = "RunRudolphFleetJob"
      effect    = "Allow"
      actions   = ["lambda:InvokeFunction"]
      resources = [statement.value]
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
