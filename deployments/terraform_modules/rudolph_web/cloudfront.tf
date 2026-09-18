#
# Optional CloudFront front end.
#
# The Function URL on its own is enough to run the app; CloudFront exists to put a custom domain and
# certificate in front of it. It caches nothing but the stylesheet: every page is rendered for the
# signed-in user and already carries Cache-Control: no-store.
#

locals {
  origin_id = "rudolph-web-function-url"
}

# Managed policies, referenced by name so the IDs stay out of the configuration.
data "aws_cloudfront_cache_policy" "disabled" {
  count = var.enable_cloudfront ? 1 : 0
  name  = "Managed-CachingDisabled"
}

data "aws_cloudfront_cache_policy" "optimized" {
  count = var.enable_cloudfront ? 1 : 0
  name  = "Managed-CachingOptimized"
}

# What reaches the origin. The session cookie, the query string carrying the OIDC callback
# parameters and the pagination cursor, and the headers Go's cross-origin protection inspects.
#
# The viewer's Host header is deliberately not forwarded: a Lambda Function URL only answers
# requests addressed to its own domain, which is why AWS ships Managed-AllViewerExceptHostHeader for
# exactly this kind of origin. The app therefore sees the Function URL's host, and takes the origin
# it trusts from RUDOLPH_WEB_REDIRECT_URL instead.
resource "aws_cloudfront_origin_request_policy" "web" {
  count = var.enable_cloudfront ? 1 : 0

  name    = "${var.prefix}-rudolph-web"
  comment = "Forwards sessions, query strings and the headers rudolph-web checks"

  cookies_config {
    cookie_behavior = "all"
  }

  query_strings_config {
    query_string_behavior = "all"
  }

  headers_config {
    header_behavior = "whitelist"

    headers {
      items = [
        "Origin",
        "Referer",
        "Authorization",
        "Accept",
        "Accept-Language",
        "Content-Type",
        "Sec-Fetch-Dest",
        "Sec-Fetch-Mode",
        "Sec-Fetch-Site",
        "Sec-Fetch-User",
        "User-Agent",
      ]
    }
  }
}

resource "aws_cloudfront_distribution" "web" {
  count = var.enable_cloudfront ? 1 : 0

  enabled         = true
  is_ipv6_enabled = true
  comment         = "${local.function_name} (Lambda Function URL origin)"
  price_class     = var.cloudfront_price_class
  aliases         = local.custom_domain ? [var.domain_name] : []

  origin {
    origin_id   = local.origin_id
    domain_name = replace(replace(aws_lambda_function_url.web.function_url, "https://", ""), "/", "")

    custom_origin_config {
      http_port              = 80
      https_port             = 443
      origin_protocol_policy = "https-only"
      origin_ssl_protocols   = ["TLSv1.2"]
    }
  }

  # Everything the app renders is per-user and must never be cached, by CloudFront or anything else.
  default_cache_behavior {
    target_origin_id       = local.origin_id
    viewer_protocol_policy = "redirect-to-https"
    compress               = true

    # POST carries the logout form and the future admin actions.
    allowed_methods = ["GET", "HEAD", "OPTIONS", "PUT", "POST", "PATCH", "DELETE"]
    cached_methods  = ["GET", "HEAD"]

    cache_policy_id          = data.aws_cloudfront_cache_policy.disabled[0].id
    origin_request_policy_id = aws_cloudfront_origin_request_policy.web[0].id
  }

  # The embedded stylesheet is the only cacheable response. It is served to anonymous requests too,
  # so nothing user specific has to reach the origin for it.
  ordered_cache_behavior {
    path_pattern           = "/static/*"
    target_origin_id       = local.origin_id
    viewer_protocol_policy = "redirect-to-https"
    compress               = true

    allowed_methods = ["GET", "HEAD"]
    cached_methods  = ["GET", "HEAD"]

    cache_policy_id = data.aws_cloudfront_cache_policy.optimized[0].id
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  viewer_certificate {
    cloudfront_default_certificate = local.certificate_arn == ""
    acm_certificate_arn            = local.certificate_arn == "" ? null : local.certificate_arn
    ssl_support_method             = local.certificate_arn == "" ? null : "sni-only"
    minimum_protocol_version       = local.certificate_arn == "" ? null : "TLSv1.2_2021"
  }

  tags = local.tags

  depends_on = [aws_acm_certificate_validation.web]
}
