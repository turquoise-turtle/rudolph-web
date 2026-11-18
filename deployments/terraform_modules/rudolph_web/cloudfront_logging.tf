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
}

# The account ID keeps the name globally unique, as S3 bucket names must be.
resource "aws_s3_bucket" "cloudfront_logs" {
  count    = local.cloudfront_logging ? 1 : 0
  provider = aws.us_east_1

  bucket = "${var.prefix}-rudolph-web-cf-logs-${var.aws_account_id}"

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

  name         = "${var.prefix}-rudolph-web-cloudfront"
  log_type     = "ACCESS_LOGS"
  resource_arn = aws_cloudfront_distribution.web[0].arn

  tags = local.tags
}

resource "aws_cloudwatch_log_delivery_destination" "cloudfront" {
  count    = local.cloudfront_logging ? 1 : 0
  provider = aws.us_east_1

  name = "${var.prefix}-rudolph-web-cloudfront-s3"

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
