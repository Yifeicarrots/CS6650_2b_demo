# Specify where to find the AWS & Docker providers
terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws" # creating AWS resources
      version = "~> 6.7.0"
    }
    docker = {
      source  = "kreuzwerker/docker" # building and pushing Docker images
      version = "~> 2.0"
    }
  }
}

# Configure AWS credentials & region
provider "aws" {
  region     = var.aws_region
}

# Fetch an ECR auth token so Terraform's Docker provider can log in
data "aws_ecr_authorization_token" "registry" {}

# Configure Docker provider to authenticate against ECR automatically
provider "docker" {
  registry_auth {
    address  = data.aws_ecr_authorization_token.registry.proxy_endpoint
    username = data.aws_ecr_authorization_token.registry.user_name
    password = data.aws_ecr_authorization_token.registry.password
  }
}
