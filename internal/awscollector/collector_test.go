package awscollector

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	apigatewayv2types "github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cloudfronttypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	cloudtrailtypes "github.com/aws/aws-sdk-go-v2/service/cloudtrail/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cloudwatchtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/smithy-go"
)

type fakeCloudWatch struct{}

func (fakeCloudWatch) GetMetricData(context.Context, *cloudwatch.GetMetricDataInput, ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	return &cloudwatch.GetMetricDataOutput{MetricDataResults: []cloudwatchtypes.MetricDataResult{{Id: aws.String("cpu"), Values: []float64{42}}}}, nil
}

type fakeEC2 struct{}

func (fakeEC2) DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	return &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{{InstanceId: aws.String("i-123"), PrivateIpAddress: aws.String("10.0.0.4")}}}}}, nil
}

type fakeCloudTrail struct{}

func (fakeCloudTrail) LookupEvents(context.Context, *cloudtrail.LookupEventsInput, ...func(*cloudtrail.Options)) (*cloudtrail.LookupEventsOutput, error) {
	return &cloudtrail.LookupEventsOutput{Events: []cloudtrailtypes.Event{{EventId: aws.String("event-1"), EventName: aws.String("RunInstances")}}}, nil
}

type fakeECS struct{}

func (fakeECS) ListClusters(context.Context, *ecs.ListClustersInput, ...func(*ecs.Options)) (*ecs.ListClustersOutput, error) {
	return &ecs.ListClustersOutput{ClusterArns: []string{"arn:aws:ecs:us-east-1:123:cluster/prod"}}, nil
}

func (fakeECS) ListServices(context.Context, *ecs.ListServicesInput, ...func(*ecs.Options)) (*ecs.ListServicesOutput, error) {
	return &ecs.ListServicesOutput{ServiceArns: []string{"arn:aws:ecs:us-east-1:123:service/prod/api"}}, nil
}

func (fakeECS) DescribeServices(context.Context, *ecs.DescribeServicesInput, ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	return &ecs.DescribeServicesOutput{Services: []ecstypes.Service{{ServiceName: aws.String("api"), ServiceArn: aws.String("arn:aws:ecs:us-east-1:123:service/prod/api"), Status: aws.String("ACTIVE"), DesiredCount: 2, RunningCount: 2}}}, nil
}

func TestCollectsPhaseOneAWSSignals(t *testing.T) {
	collector, err := New(Config{TenantID: "tenant-a", Token: "secret", IntakeURL: "http://intake", Region: "us-east-1", MetricQueries: []MetricQuery{{ID: "cpu", Namespace: "AWS/EC2", MetricName: "CPUUtilization"}}}, fakeCloudWatch{}, fakeEC2{}, fakeCloudTrail{}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	events, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("expected metric, inventory, and audit events; got %d", len(events))
	}
}

func TestCollectorDefaultsLookback(t *testing.T) {
	collector, err := New(Config{TenantID: "tenant-a", Token: "secret", IntakeURL: "http://intake"}, fakeCloudWatch{}, fakeEC2{}, fakeCloudTrail{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if collector.config.Lookback != 5*time.Minute {
		t.Fatalf("unexpected lookback: %s", collector.config.Lookback)
	}
}

func TestCollectorDeduplicatesCloudTrailAcrossCycles(t *testing.T) {
	statePath := t.TempDir() + "\\state.json"
	config := Config{TenantID: "tenant-a", Token: "secret", IntakeURL: "http://intake", StatePath: statePath}
	collector, err := New(config, fakeCloudWatch{}, fakeEC2{}, fakeCloudTrail{}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	first, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 3 || len(second) != 2 {
		t.Fatalf("expected duplicate audit event to be removed, got %d then %d events", len(first), len(second))
	}
}

func TestCollectsECSInventoryAndQueries(t *testing.T) {
	collector, err := New(Config{TenantID: "tenant-a", Token: "secret", IntakeURL: "http://intake", Region: "us-east-1"}, fakeCloudWatch{}, fakeEC2{}, fakeCloudTrail{}, slog.Default(), fakeECS{})
	if err != nil {
		t.Fatal(err)
	}
	events, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("expected EC2 inventory, ECS inventory, metric, and audit events; got %d", len(events))
	}
}

func TestCollectsRequestedServiceAdapters(t *testing.T) {
	services := &Services{
		Lambda: fakeLambda{}, RDS: fakeRDS{}, DynamoDB: fakeDynamoDB{}, SQS: fakeSQS{}, SNS: fakeSNS{}, ELB: fakeELB{}, APIGateway: fakeAPIGateway{}, CloudFront: fakeCloudFront{}, EKS: fakeEKS{},
	}
	collector, err := New(Config{TenantID: "tenant-a", Token: "secret", IntakeURL: "http://intake", Region: "us-east-1"}, fakeCloudWatch{}, fakeEC2{}, fakeCloudTrail{}, slog.Default(), services)
	if err != nil {
		t.Fatal(err)
	}
	events, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 11 {
		t.Fatalf("expected all service adapters to emit inventory and metrics, got %d events", len(events))
	}
}

type fakeLambda struct{}

func (fakeLambda) ListFunctions(context.Context, *lambda.ListFunctionsInput, ...func(*lambda.Options)) (*lambda.ListFunctionsOutput, error) {
	return &lambda.ListFunctionsOutput{Functions: []lambdatypes.FunctionConfiguration{{FunctionName: aws.String("checkout")}}}, nil
}

type fakeRDS struct{}

func (fakeRDS) DescribeDBInstances(context.Context, *rds.DescribeDBInstancesInput, ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	return &rds.DescribeDBInstancesOutput{DBInstances: []rdstypes.DBInstance{{DBInstanceIdentifier: aws.String("orders"), Engine: aws.String("postgres")}}}, nil
}

type fakeDynamoDB struct{}

func (fakeDynamoDB) ListTables(context.Context, *dynamodb.ListTablesInput, ...func(*dynamodb.Options)) (*dynamodb.ListTablesOutput, error) {
	return &dynamodb.ListTablesOutput{TableNames: []string{"orders"}}, nil
}

type fakeSQS struct{}

func (fakeSQS) ListQueues(context.Context, *sqs.ListQueuesInput, ...func(*sqs.Options)) (*sqs.ListQueuesOutput, error) {
	return &sqs.ListQueuesOutput{QueueUrls: []string{"https://sqs.us-east-1.amazonaws.com/123/orders"}}, nil
}

type fakeSNS struct{}

func (fakeSNS) ListTopics(context.Context, *sns.ListTopicsInput, ...func(*sns.Options)) (*sns.ListTopicsOutput, error) {
	return &sns.ListTopicsOutput{Topics: []snstypes.Topic{{TopicArn: aws.String("arn:aws:sns:us-east-1:123:orders")}}}, nil
}

type fakeELB struct{}

func (fakeELB) DescribeLoadBalancers(context.Context, *elasticloadbalancingv2.DescribeLoadBalancersInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error) {
	return &elasticloadbalancingv2.DescribeLoadBalancersOutput{LoadBalancers: []elbtypes.LoadBalancer{{LoadBalancerArn: aws.String("arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/orders/abc"), Type: elbtypes.LoadBalancerTypeEnumApplication}}}, nil
}

type fakeAPIGateway struct{}

func (fakeAPIGateway) GetApis(context.Context, *apigatewayv2.GetApisInput, ...func(*apigatewayv2.Options)) (*apigatewayv2.GetApisOutput, error) {
	return &apigatewayv2.GetApisOutput{Items: []apigatewayv2types.Api{{ApiId: aws.String("api-1"), Name: aws.String("orders")}}}, nil
}

type fakeCloudFront struct{}

func (fakeCloudFront) ListDistributions(context.Context, *cloudfront.ListDistributionsInput, ...func(*cloudfront.Options)) (*cloudfront.ListDistributionsOutput, error) {
	return &cloudfront.ListDistributionsOutput{DistributionList: &cloudfronttypes.DistributionList{Items: []cloudfronttypes.DistributionSummary{{Id: aws.String("EDFDVBD6EXAMPLE")}}}}, nil
}

type fakeEKS struct{}

func (fakeEKS) ListClusters(context.Context, *eks.ListClustersInput, ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
	return &eks.ListClustersOutput{Clusters: []string{"prod"}}, nil
}

// pagedCloudWatch returns one data point per page across two pages.
type pagedCloudWatch struct{ calls int }

func (c *pagedCloudWatch) GetMetricData(_ context.Context, input *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	c.calls++
	if input.NextToken == nil {
		return &cloudwatch.GetMetricDataOutput{
			MetricDataResults: []cloudwatchtypes.MetricDataResult{{Id: aws.String("cpu"), Values: []float64{1}}},
			NextToken:         aws.String("page-2"),
		}, nil
	}
	return &cloudwatch.GetMetricDataOutput{
		MetricDataResults: []cloudwatchtypes.MetricDataResult{{Id: aws.String("cpu"), Values: []float64{2}}},
	}, nil
}

func TestCollectMetricsFollowsPagination(t *testing.T) {
	cloudwatchClient := &pagedCloudWatch{}
	collector, err := New(Config{TenantID: "t", Token: "s", IntakeURL: "http://intake", MetricQueries: []MetricQuery{{ID: "cpu", Namespace: "AWS/EC2", MetricName: "CPUUtilization"}}}, cloudwatchClient, fakeEC2{}, fakeCloudTrail{}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	events, err := collector.collectMetrics(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cloudwatchClient.calls != 2 {
		t.Fatalf("expected the second page to be requested, got %d calls", cloudwatchClient.calls)
	}
	if len(events) != 2 {
		t.Fatalf("expected a data point from each page, got %d", len(events))
	}
}

type failingCloudTrail struct{}

func (failingCloudTrail) LookupEvents(context.Context, *cloudtrail.LookupEventsInput, ...func(*cloudtrail.Options)) (*cloudtrail.LookupEventsOutput, error) {
	return nil, errors.New("AccessDenied")
}

// One failing source must not discard what the other sources already returned.
func TestCollectReturnsPartialResultsWhenOneSourceFails(t *testing.T) {
	collector, err := New(Config{TenantID: "t", Token: "s", IntakeURL: "http://intake"}, fakeCloudWatch{}, fakeEC2{}, failingCloudTrail{}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	events, err := collector.Collect(context.Background())
	if err == nil {
		t.Fatal("expected the CloudTrail failure to be reported")
	}
	if len(events) == 0 {
		t.Fatal("EC2 inventory was discarded because CloudTrail failed")
	}
}

func TestSeenEventsArePrunedAndPersistedAtomically(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	collector, err := New(Config{TenantID: "t", Token: "s", IntakeURL: "http://intake", StatePath: statePath}, fakeCloudWatch{}, fakeEC2{}, fakeCloudTrail{}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	collector.seenEvents["fresh"] = time.Now().UTC()
	collector.seenEvents["stale"] = time.Now().UTC().Add(-2 * seenEventRetention)
	if err = collector.saveState(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(statePath + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temporary state file was left behind")
	}

	reloaded, err := New(Config{TenantID: "t", Token: "s", IntakeURL: "http://intake", StatePath: statePath}, fakeCloudWatch{}, fakeEC2{}, fakeCloudTrail{}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.seenEvents["fresh"]; !ok {
		t.Error("recent event ID should survive a restart")
	}
	if _, ok := reloaded.seenEvents["stale"]; ok {
		t.Error("expired event ID should have been pruned")
	}
}

// State written by the previous release is a bare array of IDs.
func TestLoadStateAcceptsLegacyFormat(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(statePath, []byte(`["event-1","event-2"]`), 0600); err != nil {
		t.Fatal(err)
	}
	collector, err := New(Config{TenantID: "t", Token: "s", IntakeURL: "http://intake", StatePath: statePath}, fakeCloudWatch{}, fakeEC2{}, fakeCloudTrail{}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if len(collector.seenEvents) != 2 {
		t.Fatalf("legacy state was not loaded: %d IDs", len(collector.seenEvents))
	}
}

func TestRetryClassificationUsesErrorCodes(t *testing.T) {
	throttled := &smithy.GenericAPIError{Code: "ThrottlingException", Message: "rate exceeded"}
	if !isRetryableAWSError(throttled) {
		t.Error("throttling should be retryable")
	}
	denied := &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "no", Fault: smithy.FaultClient}
	if isRetryableAWSError(denied) {
		t.Error("access denied should not be retried")
	}
	// Error text mentioning "timeout" must not be enough on its own.
	if isRetryableAWSError(errors.New("request timeout happened")) {
		t.Error("plain error text should not drive retry decisions")
	}
}
