output "archive_bucket" {
  value = aws_s3_bucket.archive.bucket
}

output "firehose_stream" {
  value = aws_kinesis_firehose_delivery_stream.signal.name
}
