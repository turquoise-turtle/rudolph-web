terraform {
  required_version = ">= 1.10.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.72"

      # CloudFront only accepts certificates from us-east-1, wherever the rest of the
      # deployment lives. The environment passes both providers, see environments/example/main.tf.
      configuration_aliases = [aws.us_east_1]
    }
  }
}
