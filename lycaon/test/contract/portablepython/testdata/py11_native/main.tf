variable "env" {
  type    = string
  default = "staging"
}

resource "aws_s3_bucket" "logs" {
  bucket = "pw-${var.env}-logs"
  tags = {
    owner = "platform"
    tier  = "audit"
  }
}

resource "aws_s3_bucket" "artifacts" {
  bucket = "pw-${var.env}-artifacts"
  tags = {
    owner = "build"
    tier  = "durable"
  }
}

output "logs_bucket_ref" {
  value = aws_s3_bucket.logs.bucket
}
