package awscollector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cloudwatchtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/signal-observability/collector/internal/agent"
)

type MetricQuery struct {
	ID         string
	Namespace  string
	MetricName string
	Statistic  string
	Period     int32
	Dimensions map[string]string
}

type Config struct {
	TenantID      string
	Token         string
	IntakeURL     string
	Region        string
	MetricQueries []MetricQuery
	Lookback      time.Duration
}

type CloudWatchAPI interface {
	GetMetricData(context.Context, *cloudwatch.GetMetricDataInput, ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error)
}

type EC2API interface {
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
}

type CloudTrailAPI interface {
	LookupEvents(context.Context, *cloudtrail.LookupEventsInput, ...func(*cloudtrail.Options)) (*cloudtrail.LookupEventsOutput, error)
}

type Collector struct {
	config     Config
	cloudwatch CloudWatchAPI
	ec2        EC2API
	cloudtrail CloudTrailAPI
	client     *http.Client
	log        *slog.Logger
}

func New(config Config, cloudwatchClient CloudWatchAPI, ec2Client EC2API, cloudtrailClient CloudTrailAPI, logger *slog.Logger) (*Collector, error) {
	if config.TenantID == "" || config.Token == "" || config.IntakeURL == "" {
		return nil, errors.New("tenant ID, token, and intake URL are required")
	}
	if cloudwatchClient == nil || ec2Client == nil || cloudtrailClient == nil {
		return nil, errors.New("all AWS clients are required")
	}
	if config.Lookback <= 0 {
		config.Lookback = 5 * time.Minute
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Collector{config: config, cloudwatch: cloudwatchClient, ec2: ec2Client, cloudtrail: cloudtrailClient, client: &http.Client{Timeout: 15 * time.Second}, log: logger}, nil
}

func (c *Collector) Collect(ctx context.Context) ([]agent.Event, error) {
	var events []agent.Event
	metrics, err := c.collectMetrics(ctx)
	if err != nil {
		return nil, fmt.Errorf("collect CloudWatch metrics: %w", err)
	}
	events = append(events, metrics...)
	inventory, err := c.collectInventory(ctx)
	if err != nil {
		return nil, fmt.Errorf("collect EC2 inventory: %w", err)
	}
	events = append(events, inventory...)
	audit, err := c.collectAuditEvents(ctx)
	if err != nil {
		return nil, fmt.Errorf("collect CloudTrail events: %w", err)
	}
	return append(events, audit...), nil
}

func (c *Collector) collectMetrics(ctx context.Context) ([]agent.Event, error) {
	if len(c.config.MetricQueries) == 0 {
		return nil, nil
	}
	end := time.Now().UTC()
	start := end.Add(-c.config.Lookback)
	queries := make([]cloudwatchtypes.MetricDataQuery, 0, len(c.config.MetricQueries))
	for _, query := range c.config.MetricQueries {
		dimensions := make([]cloudwatchtypes.Dimension, 0, len(query.Dimensions))
		for name, value := range query.Dimensions {
			dimensions = append(dimensions, cloudwatchtypes.Dimension{Name: aws.String(name), Value: aws.String(value)})
		}
		statistic := query.Statistic
		if statistic == "" {
			statistic = "Average"
		}
		period := query.Period
		if period <= 0 {
			period = 60
		}
		queries = append(queries, cloudwatchtypes.MetricDataQuery{
			Id:         aws.String(query.ID),
			MetricStat: &cloudwatchtypes.MetricStat{Metric: &cloudwatchtypes.Metric{Namespace: aws.String(query.Namespace), MetricName: aws.String(query.MetricName), Dimensions: dimensions}, Period: aws.Int32(period), Stat: aws.String(statistic)},
			ReturnData: aws.Bool(true),
		})
	}
	output, err := c.cloudwatch.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{MetricDataQueries: queries, StartTime: &start, EndTime: &end})
	if err != nil {
		return nil, err
	}
	var events []agent.Event
	for _, result := range output.MetricDataResults {
		for index, value := range result.Values {
			payload := map[string]any{"source": "aws.cloudwatch", "region": c.config.Region, "query_id": aws.ToString(result.Id), "value": value}
			if index < len(result.Timestamps) {
				payload["timestamp"] = result.Timestamps[index]
			}
			events = append(events, event("metrics", payload))
		}
	}
	return events, nil
}

func (c *Collector) collectInventory(ctx context.Context) ([]agent.Event, error) {
	output, err := c.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{})
	if err != nil {
		return nil, err
	}
	var events []agent.Event
	for _, reservation := range output.Reservations {
		for _, instance := range reservation.Instances {
			payload := map[string]any{"source": "aws.ec2", "region": c.config.Region, "instance_id": aws.ToString(instance.InstanceId), "instance_type": instance.InstanceType, "state": instance.State, "private_ip": aws.ToString(instance.PrivateIpAddress), "availability_zone": availabilityZone(instance)}
			events = append(events, event("metrics", payload))
		}
	}
	return events, nil
}

func availabilityZone(instance types.Instance) string {
	if instance.Placement == nil {
		return ""
	}
	return aws.ToString(instance.Placement.AvailabilityZone)
}

func (c *Collector) collectAuditEvents(ctx context.Context) ([]agent.Event, error) {
	start := time.Now().UTC().Add(-c.config.Lookback)
	output, err := c.cloudtrail.LookupEvents(ctx, &cloudtrail.LookupEventsInput{StartTime: &start, EndTime: awsTime(time.Now().UTC()), MaxResults: aws.Int32(50)})
	if err != nil {
		return nil, err
	}
	var events []agent.Event
	for _, audit := range output.Events {
		payload := map[string]any{"source": "aws.cloudtrail", "region": c.config.Region, "event_id": aws.ToString(audit.EventId), "event_name": aws.ToString(audit.EventName), "username": aws.ToString(audit.Username), "event_time": audit.EventTime, "cloud_trail_event": aws.ToString(audit.CloudTrailEvent)}
		events = append(events, event("logs", payload))
	}
	return events, nil
}

func awsTime(value time.Time) *time.Time { return &value }

func event(eventType string, payload any) agent.Event {
	body, _ := json.Marshal(payload)
	return agent.Event{Type: eventType, Timestamp: time.Now().UTC(), Payload: body}
}

func (c *Collector) Send(ctx context.Context, events []agent.Event) error {
	if len(events) == 0 {
		return nil
	}
	body, err := json.Marshal(agent.Envelope{TenantID: c.config.TenantID, Events: events})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.IntakeURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.config.Token)
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("intake returned HTTP %d", response.StatusCode)
	}
	return nil
}
