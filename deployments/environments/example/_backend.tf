terraform {
  backend "s3" {
    # The S3 bucket holding the Terraform state. It is not created by this configuration and must
    # already exist.
    bucket = "your-terraform-tfstate-bucket"

    # Unique within the bucket; include the environment name.
    key = "rudolph-web-example.tfstate"

    region  = "us-east-1"
    encrypt = true

    # The KMS key used for server-side encryption of the state, where applicable.
    kms_key_id = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
  }
}

provider "aws" {
  region = "us-east-1"

  default_tags {
    tags = {
      Name = "rudolph-web"
    }
  }
}

# CloudFront certificates must live in us-east-1. Keep this provider even when the deployment lives
# elsewhere, and even with CloudFront disabled: the module declares it.
provider "aws" {
  alias  = "us_east_1"
  region = "us-east-1"

  default_tags {
    tags = {
      Name = "rudolph-web"
    }
  }
}
