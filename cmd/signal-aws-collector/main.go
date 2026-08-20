package main

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/signal-observability/collector/internal/awscollector"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "us-east-1"
	}
	config, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(region))
	if err != nil {
		logger.Error("AWS configuration failed", "error", err)
		os.Exit(1)
	}
	if roleARN := os.Getenv("SIGNAL_AWS_ROLE_ARN"); roleARN != "" {
		externalID := os.Getenv("SIGNAL_AWS_EXTERNAL_ID")
		provider := stscreds.NewAssumeRoleProvider(sts.NewFromConfig(config), roleARN, func(options *stscreds.AssumeRoleOptions) {
			if externalID != "" {
				options.ExternalID = aws.String(externalID)
			}
		})
		config.Credentials = aws.NewCredentialsCache(provider)
		logger.Info("using cross-account AWS role", "role_arn", roleARN)
	}
	collector, err := awscollector.New(awscollector.Config{TenantID: os.Getenv("SIGNAL_TENANT_ID"), Token: os.Getenv("SIGNAL_INGEST_TOKEN"), IntakeURL: os.Getenv("SIGNAL_INTAKE_URL"), Region: region, Lookback: lookback(), StatePath: os.Getenv("SIGNAL_AWS_STATE_PATH")}, cloudwatch.NewFromConfig(config), ec2.NewFromConfig(config), cloudtrail.NewFromConfig(config), logger, ecs.NewFromConfig(config))
	if err != nil {
		logger.Error("AWS collector configuration failed", "error", err)
		os.Exit(1)
	}
	interval := 60 * time.Second
	if value, parseErr := strconv.Atoi(os.Getenv("SIGNAL_AWS_POLL_INTERVAL_SECONDS")); parseErr == nil && value > 0 {
		interval = time.Duration(value) * time.Second
	}
	for {
		ctx, cancel := context.WithTimeout(context.Background(), interval)
		events, collectErr := collector.Collect(ctx)
		cancel()
		if collectErr != nil {
			logger.Error("AWS collection failed", "error", collectErr)
		} else if sendErr := collector.Send(context.Background(), events); sendErr != nil {
			logger.Error("AWS intake delivery failed", "error", sendErr)
		} else {
			logger.Info("AWS telemetry delivered", "events", len(events), "region", region)
		}
		time.Sleep(interval)
	}
}

func lookback() time.Duration {
	value, err := strconv.Atoi(os.Getenv("SIGNAL_AWS_LOOKBACK_SECONDS"))
	if err == nil && value > 0 {
		return time.Duration(value) * time.Second
	}
	return 300 * time.Second
}
