# Signal AWS ingestion Terraform

This module provisions the AWS-native delivery paths into Signal:

- CloudWatch Logs subscription filters to Kinesis Data Firehose
- Firehose delivery to an S3 archive bucket
- EventBridge AWS event rule to an HTTPS API Destination

The Signal intake gateway must be reachable through HTTPS. The intake bearer token is read from Secrets Manager at apply time.

## Example

```hcl
module "signal_ingestion" {
  source = "./terraform/aws-ingestion"

  intake_base_url          = "https://intake.example.com"
  intake_token_secret_arn  = "arn:aws:secretsmanager:us-east-1:123456789012:secret:signal-intake-token"
  cloudwatch_log_group_names = [
    "/aws/lambda/orders",
    "/aws/ecs/checkout",
  ]
}
```

Review IAM, bucket encryption, retention, network access, and organization policies before applying in production. This module is a starting point and does not create the public/private networking required to expose the intake gateway.