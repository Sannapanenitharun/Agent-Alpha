# AWS observability coverage

Signal will cover AWS through a service-adapter model. Each adapter emits the same normalized metrics, logs, traces, events, and resource-identity records into the intake gateway.

## Collection paths

1. **Workload-local collection**: the Signal agent or OpenTelemetry Collector runs on EC2, ECS, EKS, or customer hosts. It collects host metrics, application OTLP, container logs, and process data.
2. **AWS control-plane collection**: a regional AWS collector assumes a customer-approved IAM role and reads CloudWatch metrics, CloudTrail events, resource inventory, tags, health events, and service APIs.
3. **Native log collection**: Signal consumes CloudWatch Logs subscriptions, Kinesis Data Firehose, S3 exports, or EventBridge events. High-volume sources must use a stream or subscription rather than polling.

## Service coverage matrix

| Family | Initial services | Primary data | Collection path |
|---|---|---|---|
| Compute | EC2, Auto Scaling, Lambda, Batch, Lightsail | Metrics, events, inventory, logs | CloudWatch/API + local agent |
| Containers | ECS, EKS, Fargate, ECR, App Runner | Metrics, logs, events, workload identity | CloudWatch/API + OTel/Kubernetes |
| Web and app hosting | Elastic Beanstalk, Amplify, API Gateway, CloudFront, AppSync | Metrics, access logs, deployment events | CloudWatch/API + S3/Kinesis |
| Queues and messaging | SQS, SNS, EventBridge, Kinesis, MSK, MQ, Amazon SES | Throughput, backlog, failures, delivery events | CloudWatch/API + native events |
| Databases | RDS, Aurora, DynamoDB, ElastiCache, Neptune, OpenSearch, Redshift | Performance metrics, capacity, slow logs, events | CloudWatch/API + log subscriptions |
| Storage | S3, EFS, FSx, Storage Gateway, Backup | Capacity, requests, errors, inventory, audit events | CloudWatch/API + CloudTrail/S3 |
| Networking | VPC, Transit Gateway, NAT Gateway, ELB/ALB/NLB, Route 53, Direct Connect, VPN, PrivateLink | Flow logs, health, latency, traffic, DNS events | CloudWatch/API + VPC Flow Logs |
| Security and identity | IAM, KMS, WAF, Shield, GuardDuty, Inspector, Macie, Security Hub, Cognito | Findings, audit events, key usage, policy changes | CloudTrail/EventBridge + APIs |
| Management | CloudTrail, Config, Systems Manager, Organizations, Control Tower, Service Catalog | Audit, compliance, inventory, changes | Native AWS event streams |
| Data and analytics | Glue, Athena, EMR, Lake Formation, MWAA, Data Firehose | Job state, latency, throughput, failures | CloudWatch/API + logs |
| Developer tools | CodeBuild, CodeDeploy, CodePipeline, CodeCommit, CloudFormation, CDK | Build/deploy state and failures | CloudWatch/API + EventBridge |
| AI and ML | SageMaker, Bedrock, Textract, Comprehend, Rekognition | Invocation, latency, token/compute usage, errors | CloudWatch/API + service events |

## Adapter contract

Every AWS adapter must provide:

- `resource.id`, `cloud.provider=aws`, `cloud.region`, `cloud.account.id`
- AWS tags when available
- CloudWatch metric namespace and dimensions
- Event source and event ID for auditability
- Collection timestamp and source timestamp
- API error counters and throttling metrics
- Rate-limit handling with exponential backoff
- Region and account scoping
- Least-privilege IAM policy documentation

## Architecture

```text
AWS account(s)
  |
  +-- CloudWatch metrics and alarms
  +-- CloudTrail / EventBridge events
  +-- CloudWatch Logs subscriptions
  +-- S3 / Firehose exports
  +-- AWS service APIs
  +-- Signal agent on EC2/ECS/EKS
        |
        v
AWS collector gateway
  |
  +-- account and region scheduler
  +-- IAM role assumption
  +-- throttling and retry control
  +-- resource identity and tag enrichment
  +-- deduplication and checkpointing
        |
        v
Signal intake gateway -> buffers -> metrics/logs/traces storage -> dashboard
```

      The AWS collector is an independent, opt-in service. Run it locally with `docker compose --profile aws up --build`, or deploy the image to ECS/Fargate with an ECS task role. For cross-account monitoring, set `SIGNAL_AWS_ROLE_ARN` and `SIGNAL_AWS_EXTERNAL_ID`; the collector uses the default AWS credential chain for its own account and STS `AssumeRole` for a customer account.

## Rollout phases

### Phase 1: foundational AWS coverage

EC2, ECS, EKS, Lambda, RDS/Aurora, DynamoDB, S3, SQS, SNS, API Gateway, ALB/NLB, CloudFront, CloudWatch Logs, CloudTrail, and EventBridge.

### Phase 2: platform depth

ElastiCache, OpenSearch, Redshift, Kinesis, MSK, ECR, EFS, NAT Gateway, Transit Gateway, Route 53, WAF, GuardDuty, Security Hub, Systems Manager, and CodePipeline.

### Phase 3: breadth and specialized services

Glue, EMR, MWAA, SageMaker, Bedrock, Lake Formation, Neptune, FSx, Backup, Organizations, Control Tower, and remaining regional services.

## Production rules

- Never use long-lived AWS access keys in an agent or collector.
- Prefer cross-account `sts:AssumeRole` with an external ID and an allowlisted collector role.
- Use CloudWatch metric APIs for low-cardinality service metrics and streams/subscriptions for logs and events.
- Cache resource inventory and tags; do not enumerate every resource on every poll.
- Enforce per-account and per-region API budgets.
- Treat CloudTrail, security findings, and application logs as separate retention and access classes.
- Keep AWS collection optional and independently deployable from the host agent.
