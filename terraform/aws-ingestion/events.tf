data "aws_secretsmanager_secret_version" "intake_token" {
  secret_id = var.intake_token_secret_arn
}

resource "aws_cloudwatch_event_rule" "signal" {
  count         = var.enable_eventbridge ? 1 : 0
  name          = "${var.name}-aws-events"
  event_pattern = jsonencode({ source = [{ prefix = "aws." }] })
}

resource "aws_cloudwatch_event_connection" "signal" {
  count              = var.enable_eventbridge ? 1 : 0
  name               = "${var.name}-intake"
  authorization_type = "API_KEY"
  auth_parameters {
    api_key {
      key   = "Authorization"
      value = "Bearer ${data.aws_secretsmanager_secret_version.intake_token.secret_string}"
    }
  }
}

resource "aws_cloudwatch_event_api_destination" "signal" {
  count               = var.enable_eventbridge ? 1 : 0
  name                = "${var.name}-intake"
  connection_arn      = aws_cloudwatch_event_connection.signal[0].arn
  invocation_endpoint = "${var.intake_base_url}/v1/aws/eventbridge"
  http_method         = "POST"
}

resource "aws_cloudwatch_event_target" "signal" {
  count     = var.enable_eventbridge ? 1 : 0
  rule      = aws_cloudwatch_event_rule.signal[0].name
  target_id = "signal-intake"
  arn       = aws_cloudwatch_event_api_destination.signal[0].arn
  role_arn  = aws_iam_role.eventbridge_invoke.arn
}

resource "aws_cloudwatch_log_subscription_filter" "signal" {
  for_each        = toset(var.cloudwatch_log_group_names)
  name            = "${var.name}-signal"
  log_group_name  = each.value
  filter_pattern  = ""
  destination_arn = aws_kinesis_firehose_delivery_stream.signal.arn
  role_arn        = aws_iam_role.cloudwatch_to_firehose.arn
}
