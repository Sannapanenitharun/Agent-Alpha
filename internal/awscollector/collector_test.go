package awscollector

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	cloudtrailtypes "github.com/aws/aws-sdk-go-v2/service/cloudtrail/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cloudwatchtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
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
