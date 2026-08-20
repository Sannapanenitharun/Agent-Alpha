module github.com/signal-observability/collector

go 1.25.0

require (
	github.com/aws/aws-sdk-go-v2 v1.43.6
	github.com/aws/aws-sdk-go-v2/config v1.32.37
	github.com/aws/aws-sdk-go-v2/credentials v1.19.36
	github.com/aws/aws-sdk-go-v2/service/apigatewayv2 v1.37.6
	github.com/aws/aws-sdk-go-v2/service/cloudfront v1.67.6
	github.com/aws/aws-sdk-go-v2/service/cloudtrail v1.58.6
	github.com/aws/aws-sdk-go-v2/service/cloudwatch v1.66.5
	github.com/aws/aws-sdk-go-v2/service/dynamodb v1.63.3
	github.com/aws/aws-sdk-go-v2/service/ec2 v1.321.3
	github.com/aws/aws-sdk-go-v2/service/ecs v1.90.2
	github.com/aws/aws-sdk-go-v2/service/eks v1.92.0
	github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2 v1.58.7
	github.com/aws/aws-sdk-go-v2/service/lambda v1.101.4
	github.com/aws/aws-sdk-go-v2/service/rds v1.124.3
	github.com/aws/aws-sdk-go-v2/service/sns v1.42.6
	github.com/aws/aws-sdk-go-v2/service/sqs v1.46.6
	github.com/aws/aws-sdk-go-v2/service/sts v1.45.6
	go.opentelemetry.io/proto/otlp v1.11.0
	google.golang.org/grpc v1.82.1
	google.golang.org/protobuf v1.36.12
)

require (
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.18 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.18.37 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.37 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.37 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.38 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.17 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/endpoint-discovery v1.12.14 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.37 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.5.6 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.33.6 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.38.6 // indirect
	github.com/aws/smithy-go v1.27.8 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.29.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260720211330-0afa2a65878a // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260720211330-0afa2a65878a // indirect
)
