#
# Certificate and DNS for the custom domain. Only used when CloudFront is enabled with a domain_name;
# Route 53 is not required, and with route53_zone_name empty you create the validation record and the
# alias yourself.
#

locals {
  certificate_arn = var.acm_certificate_arn != "" ? var.acm_certificate_arn : (
    local.create_certificate ? aws_acm_certificate.web[0].arn : ""
  )

  route53_zone_id = local.route53_enabled ? (
    var.use_existing_route53_zone ? data.aws_route53_zone.existing[0].zone_id : aws_route53_zone.new[0].zone_id
  ) : ""

  validate_with_route53 = local.create_certificate && local.route53_enabled
}

# CloudFront only accepts certificates from us-east-1.
resource "aws_acm_certificate" "web" {
  count    = local.create_certificate ? 1 : 0
  provider = aws.us_east_1

  domain_name       = var.domain_name
  validation_method = "DNS"
  tags              = local.tags

  lifecycle {
    create_before_destroy = true
  }
}

data "aws_route53_zone" "existing" {
  count = local.route53_enabled && var.use_existing_route53_zone ? 1 : 0
  name  = var.route53_zone_name
}

resource "aws_route53_zone" "new" {
  count = local.route53_enabled && !var.use_existing_route53_zone ? 1 : 0
  name  = var.route53_zone_name
  tags  = local.tags
}

# One domain, so one validation record; taking the first option keeps this readable.
locals {
  domain_validation_options = local.create_certificate ? tomap(tolist(aws_acm_certificate.web[0].domain_validation_options)[0]) : {}
}

resource "aws_route53_record" "certificate_validation" {
  count = local.validate_with_route53 ? 1 : 0

  zone_id         = local.route53_zone_id
  name            = lookup(local.domain_validation_options, "resource_record_name", "")
  type            = lookup(local.domain_validation_options, "resource_record_type", "")
  records         = [lookup(local.domain_validation_options, "resource_record_value", "")]
  ttl             = 300
  allow_overwrite = true
}

resource "aws_acm_certificate_validation" "web" {
  count    = local.validate_with_route53 ? 1 : 0
  provider = aws.us_east_1

  certificate_arn         = aws_acm_certificate.web[0].arn
  validation_record_fqdns = [aws_route53_record.certificate_validation[0].fqdn]
}

resource "aws_route53_record" "web" {
  for_each = local.custom_domain && local.route53_enabled ? toset(["A", "AAAA"]) : toset([])

  zone_id = local.route53_zone_id
  name    = var.domain_name
  type    = each.value

  alias {
    name                   = aws_cloudfront_distribution.web[0].domain_name
    zone_id                = aws_cloudfront_distribution.web[0].hosted_zone_id
    evaluate_target_health = false
  }
}
