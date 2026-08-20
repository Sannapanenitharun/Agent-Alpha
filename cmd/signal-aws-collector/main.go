package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/signal-observability/collector/internal/agent"
	"github.com/signal-observability/collector/internal/awscollector"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	serviceConfig, configErr := awscollector.LoadServiceConfig(os.Getenv("SIGNAL_AWS_CONFIG_PATH"))
	if configErr != nil && os.Getenv("SIGNAL_AWS_CONFIG_PATH") != "" {
		logger.Error("AWS service configuration failed", "error", configErr)
		os.Exit(1)
	}
	interval := serviceConfig.Collection.PollInterval
	if interval <= 0 {
		interval = 60 * time.Second
	}
	if value, parseErr := strconv.Atoi(os.Getenv("SIGNAL_AWS_POLL_INTERVAL_SECONDS")); parseErr == nil && value > 0 {
		interval = time.Duration(value) * time.Second
	}
	regions := serviceConfig.Collection.Regions
	if configuredRegion := os.Getenv("AWS_REGION"); configuredRegion != "" {
		regions = []string{configuredRegion}
	}
	if len(regions) == 0 {
		regions = []string{"us-east-1"}
	}
	for {
		for _, region := range regions {
			ctx, cancel := context.WithTimeout(context.Background(), interval)
			events, err := collectRegion(ctx, region, serviceConfig, logger)
			cancel()
			if err != nil {
				logger.Error("AWS collection failed", "error", err, "region", region)
				continue
			}
			logger.Info("AWS telemetry delivered", "events", len(events), "region", region)
		}
		time.Sleep(interval)
	}
}

func collectRegion(ctx context.Context, region string, serviceConfig awscollector.ServiceConfig, logger *slog.Logger) ([]agent.Event, error) {
	config, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("AWS configuration: %w", err)
	}
	if roleARN := os.Getenv("SIGNAL_AWS_ROLE_ARN"); roleARN != "" {
		externalID := os.Getenv("SIGNAL_AWS_EXTERNAL_ID")
		provider := stscreds.NewAssumeRoleProvider(sts.NewFromConfig(config), roleARN, func(options *stscreds.AssumeRoleOptions) {
			if externalID != "" {
				options.ExternalID = aws.String(externalID)
			}
		})
		config.Credentials = aws.NewCredentialsCache(provider)
	}
	identity, err := sts.NewFromConfig(config).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, fmt.Errorf("AWS identity lookup: %w", err)
	}
	services := &awscollector.Services{Lambda: lambda.NewFromConfig(config), RDS: rds.NewFromConfig(config), DynamoDB: dynamodb.NewFromConfig(config), SQS: sqs.NewFromConfig(config), SNS: sns.NewFromConfig(config), ELB: elasticloadbalancingv2.NewFromConfig(config), APIGateway: apigatewayv2.NewFromConfig(config), CloudFront: cloudfront.NewFromConfig(config), EKS: eks.NewFromConfig(config)}
	statePath := os.Getenv("SIGNAL_AWS_STATE_PATH")
	if statePath != "" {
		statePath = strings.ReplaceAll(statePath, ".json", "-"+region+".json")
	}
	collector, err := awscollector.New(awscollector.Config{TenantID: os.Getenv("SIGNAL_TENANT_ID"), Token: os.Getenv("SIGNAL_INGEST_TOKEN"), IntakeURL: os.Getenv("SIGNAL_INTAKE_URL"), Region: region, AccountID: aws.ToString(identity.Account), Lookback: lookback(), StatePath: statePath}, cloudwatch.NewFromConfig(config), ec2.NewFromConfig(config), cloudtrail.NewFromConfig(config), logger, ecs.NewFromConfig(config), services)
	if err != nil {
		return nil, err
	}
	events, err := collector.Collect(ctx)
	if err != nil {
		return nil, err
	}
	if err := collector.Send(ctx, events); err != nil {
		return nil, fmt.Errorf("intake delivery: %w", err)
	}
	return events, nil
}

func lookback() time.Duration {
	value, err := strconv.Atoi(os.Getenv("SIGNAL_AWS_LOOKBACK_SECONDS"))
	if err == nil && value > 0 {
		return time.Duration(value) * time.Second
	}
	return 300 * time.Second
}
