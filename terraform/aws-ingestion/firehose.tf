resource "aws_s3_bucket" "archive" {
  bucket_prefix = "${var.name}-telemetry-"
}

resource "aws_kinesis_firehose_delivery_stream" "signal" {
  name        = "${var.name}-cloudwatch-logs"
  destination = "extended_s3"

  extended_s3_configuration {
    role_arn           = aws_iam_role.firehose.arn
    bucket_arn         = aws_s3_bucket.archive.arn
    buffering_interval = 60
    buffering_size     = 5
    compression_format = "GZIP"
    prefix             = "cloudwatch-logs/"
  }
}

resource "aws_iam_role" "firehose" {
  name = "${var.name}-firehose"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{ Effect = "Allow", Principal = { Service = "firehose.amazonaws.com" }, Action = "sts:AssumeRole" }]
  })
}

resource "aws_iam_role_policy" "firehose" {
  role = aws_iam_role.firehose.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{ Effect = "Allow", Action = ["s3:AbortMultipartUpload", "s3:GetBucketLocation", "s3:ListBucket", "s3:PutObject"], Resource = [aws_s3_bucket.archive.arn, "${aws_s3_bucket.archive.arn}/*"] }]
  })
}

resource "aws_iam_role" "cloudwatch_to_firehose" {
  name = "${var.name}-cloudwatch-firehose"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{ Effect = "Allow", Principal = { Service = "logs.${var.aws_region}.amazonaws.com" }, Action = "sts:AssumeRole" }]
  })
}

resource "aws_iam_role_policy" "cloudwatch_to_firehose" {
  role = aws_iam_role.cloudwatch_to_firehose.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{ Effect = "Allow", Action = ["firehose:PutRecord", "firehose:PutRecordBatch"], Resource = aws_kinesis_firehose_delivery_stream.signal.arn }]
  })
}

resource "aws_iam_role" "eventbridge_invoke" {
  name = "${var.name}-eventbridge"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{ Effect = "Allow", Principal = { Service = "events.amazonaws.com" }, Action = "sts:AssumeRole" }]
  })
}

resource "aws_iam_role_policy" "eventbridge_invoke" {
  role = aws_iam_role.eventbridge_invoke.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{ Effect = "Allow", Action = ["events:InvokeApiDestination"], Resource = var.enable_eventbridge ? aws_cloudwatch_event_api_destination.signal[0].arn : "arn:aws:events:${var.aws_region}:000000000000:api-destination/disabled" }]
  })
}
