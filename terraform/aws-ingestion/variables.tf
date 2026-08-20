variable "aws_region" {
  type    = string
  default = "us-east-1"
}

variable "name" {
  type    = string
  default = "signal"
}

variable "intake_base_url" {
  type        = string
  description = "HTTPS base URL for the Signal intake gateway, without a trailing slash."
}

variable "intake_token_secret_arn" {
  type        = string
  description = "Secrets Manager ARN containing the Signal intake bearer token."
}

variable "cloudwatch_log_group_names" {
  type    = list(string)
  default = []
}

variable "enable_eventbridge" {
  type    = bool
  default = true
}

variable "enable_s3_notifications" {
  type    = bool
  default = false
}
