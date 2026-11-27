resource "aws_s3_bucket" "broken" {
  bucket = "unterminated-block"
  tags = {
    owner = "missing-closing-brace"
