#
# Optional CloudFront access logs, with enable_cloudfront_logging.
#
# Delivered by CloudFront standard logging v2 (CloudWatch Logs vended log deliveries) rather than the
# distribution's legacy logging_config: legacy logging writes each object with an ACL grant to
# CloudFront's log delivery account, which needs ACLs enabled on the bucket, while v2 delivers through
# a bucket policy and leaves ACLs disabled (BucketOwnerEnforced, the S3 default).
#
# CloudFront's deliveries can only be configured in us-east-1, so the bucket is created there too,
# whatever region the rest of the deployment is in.
#

locals {
  cloudfront_logging = var.enable_cloudfront && var.enable_cloudfront_logging

  # var.prefix is free-form, and existing deployments use prefixes such as my_org or Prod, which S3 refuses
  # in a bucket name (lowercase letters, digits, dots and hyphens, 3 to 63 characters). A prefix that is
  # already valid, short enough and not reserved by S3 (xn--, sthree-) is used as it is; any other is lowercased,
  # made S3-safe and cut short, and an 8-character hash of the original keeps two prefixes that come out the
  # same (my_org and my-org) apart.
  # The account ID keeps the name globally unique, as S3 bucket names must be.
  log_bucket_prefix_as_is = can(regex("^[a-z0-9][a-z0-9-]{0,29}$", var.prefix)) && !endswith(var.prefix, "-") && !startswith(var.prefix, "xn--") && !startswith(var.prefix, "sthree-")
  log_bucket_safe_prefix  = trim(substr(trim(replace(lower(var.prefix), "/[^a-z0-9]+/", "-"), "-"), 0, 24), "-")
  log_bucket_name = (local.log_bucket_prefix_as_is
    ? "${var.prefix}-rudolph-web-cf-logs-${var.aws_account_id}"
    : "${local.log_bucket_safe_prefix == "" || startswith(local.log_bucket_safe_prefix, "sthree-") ? trimsuffix("rw-${local.log_bucket_safe_prefix}", "-") : local.log_bucket_safe_prefix}-rw-cf-logs-${substr(sha1(var.prefix), 0, 8)}-${var.aws_account_id}"
  )

  # Log delivery source and destination names are letters, digits, _ and -, at most 60 characters.
  log_delivery_safe_prefix = replace(var.prefix, "/[^A-Za-z0-9_-]/", "-")
  log_delivery_prefix = (length(local.log_delivery_safe_prefix) <= 29 && local.log_delivery_safe_prefix == var.prefix
    ? var.prefix
    : "${substr(local.log_delivery_safe_prefix, 0, 20)}-${substr(sha1(var.prefix), 0, 8)}"
  )
}

resource "aws_s3_bucket" "cloudfront_logs" {
  count    = local.cloudfront_logging ? 1 : 0
  provider = aws.us_east_1

  bucket = local.log_bucket_name

  tags = local.tags
}

resource "aws_s3_bucket_public_access_block" "cloudfront_logs" {
  count    = local.cloudfront_logging ? 1 : 0
  provider = aws.us_east_1

  bucket = aws_s3_bucket.cloudfront_logs[0].id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# SSE-S3 rather than a KMS key: log delivery to an SSE-KMS bucket needs the key policy to admit the
# delivery service as well, and access logs need no key of their own.
resource "aws_s3_bucket_server_side_encryption_configuration" "cloudfront_logs" {
  count    = local.cloudfront_logging ? 1 : 0
  provider = aws.us_east_1

  bucket = aws_s3_bucket.cloudfront_logs[0].id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# The bucket is deliberately unversioned, so an expired log is gone rather than kept as a noncurrent
# version.
resource "aws_s3_bucket_lifecycle_configuration" "cloudfront_logs" {
  count    = local.cloudfront_logging ? 1 : 0
  provider = aws.us_east_1

  bucket = aws_s3_bucket.cloudfront_logs[0].id

  rule {
    id     = "expire-access-logs"
    status = "Enabled"

    filter {}

    expiration {
      days = var.cloudfront_log_retention_days
    }

    abort_incomplete_multipart_upload {
      days_after_initiation = 1
    }
  }
}

# Written with jsonencode rather than an aws_iam_policy_document: the TLS-only statement has to deny
# s3:*, and the module's policy documents are held to granting no wildcard actions.
resource "aws_s3_bucket_policy" "cloudfront_logs" {
  count    = local.cloudfront_logging ? 1 : 0
  provider = aws.us_east_1

  bucket = aws_s3_bucket.cloudfront_logs[0].id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid       = "DenyInsecureTransport"
        Effect    = "Deny"
        Principal = "*"
        Action    = "s3:*"
        Resource = [
          aws_s3_bucket.cloudfront_logs[0].arn,
          "${aws_s3_bucket.cloudfront_logs[0].arn}/*",
        ]
        Condition = {
          Bool = { "aws:SecureTransport" = "false" }
        }
      },
      {
        # Only this deployment's own delivery source, in this account, may write logs.
        Sid       = "AllowCloudFrontLogDelivery"
        Effect    = "Allow"
        Principal = { Service = "delivery.logs.amazonaws.com" }
        Action    = "s3:PutObject"
        Resource  = "${aws_s3_bucket.cloudfront_logs[0].arn}/*"
        Condition = {
          StringEquals = {
            "s3:x-amz-acl"      = "bucket-owner-full-control"
            "aws:SourceAccount" = var.aws_account_id
          }
          ArnLike = {
            "aws:SourceArn" = aws_cloudwatch_log_delivery_source.cloudfront[0].arn
          }
        }
      },
    ]
  })

  # S3 can answer OperationAborted when a bucket's policy and public access block are set at once.
  depends_on = [aws_s3_bucket_public_access_block.cloudfront_logs]
}

resource "aws_cloudwatch_log_delivery_source" "cloudfront" {
  count    = local.cloudfront_logging ? 1 : 0
  provider = aws.us_east_1

  name         = "${local.log_delivery_prefix}-rudolph-web-cloudfront"
  log_type     = "ACCESS_LOGS"
  resource_arn = aws_cloudfront_distribution.web[0].arn

  tags = local.tags
}

resource "aws_cloudwatch_log_delivery_destination" "cloudfront" {
  count    = local.cloudfront_logging ? 1 : 0
  provider = aws.us_east_1

  name = "${local.log_delivery_prefix}-rudolph-web-cloudfront-s3"

  # W3C is the layout of CloudFront's legacy access logs, which existing log tooling reads.
  output_format = "w3c"

  delivery_destination_configuration {
    destination_resource_arn = aws_s3_bucket.cloudfront_logs[0].arn
  }

  tags = local.tags
}

resource "aws_cloudwatch_log_delivery" "cloudfront" {
  count    = local.cloudfront_logging ? 1 : 0
  provider = aws.us_east_1

  delivery_source_name     = aws_cloudwatch_log_delivery_source.cloudfront[0].name
  delivery_destination_arn = aws_cloudwatch_log_delivery_destination.cloudfront[0].arn

  tags = local.tags

  # Nothing can be delivered until the bucket policy admits the delivery service.
  depends_on = [aws_s3_bucket_policy.cloudfront_logs]
}
