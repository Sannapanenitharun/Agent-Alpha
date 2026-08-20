package awscollector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
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
	StatePath     string
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
	seenEvents map[string]struct{}
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
	collector := &Collector{config: config, cloudwatch: cloudwatchClient, ec2: ec2Client, cloudtrail: cloudtrailClient, client: &http.Client{Timeout: 15 * time.Second}, log: logger, seenEvents: map[string]struct{}{}}
	if config.StatePath != "" {
		if err := collector.loadState(); err != nil {
			return nil, err
		}
	}
	return collector, nil
}

func (c *Collector) Collect(ctx context.Context) ([]agent.Event, error) {
	var events []agent.Event
	inventory, discoveredQueries, err := c.collectInventory(ctx)
	if err != nil {
		return nil, fmt.Errorf("collect EC2 inventory: %w", err)
	}
	events = append(events, inventory...)
	metrics, err := c.collectMetrics(ctx, discoveredQueries)
	if err != nil {
		return nil, fmt.Errorf("collect CloudWatch metrics: %w", err)
	}
	events = append(events, metrics...)
	audit, err := c.collectAuditEvents(ctx)
	if err != nil {
		return nil, fmt.Errorf("collect CloudTrail events: %w", err)
	}
	events = append(events, audit...)
	if c.config.StatePath != "" {
		if err := c.saveState(); err != nil {
			return nil, err
		}
	}
	return events, nil
}

func (c *Collector) collectMetrics(ctx context.Context, discovered []MetricQuery) ([]agent.Event, error) {
	queriesToRun := append(append([]MetricQuery(nil), c.config.MetricQueries...), discovered...)
	if len(queriesToRun) == 0 {
		return nil, nil
	}
	end := time.Now().UTC()
	start := end.Add(-c.config.Lookback)
	queries := make([]cloudwatchtypes.MetricDataQuery, 0, len(queriesToRun))
	for _, query := range queriesToRun {
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

func (c *Collector) collectInventory(ctx context.Context) ([]agent.Event, []MetricQuery, error) {
	var events []agent.Event
	var queries []MetricQuery
	input := &ec2.DescribeInstancesInput{}
	for {
		output, err := c.ec2.DescribeInstances(ctx, input)
		if err != nil {
			return nil, nil, err
		}
		for _, reservation := range output.Reservations {
			for _, instance := range reservation.Instances {
				instanceID := aws.ToString(instance.InstanceId)
				payload := map[string]any{"source": "aws.ec2", "region": c.config.Region, "resource_type": "ec2_instance", "instance_id": instanceID, "instance_type": instance.InstanceType, "state": instance.State, "private_ip": aws.ToString(instance.PrivateIpAddress), "availability_zone": availabilityZone(instance)}
				events = append(events, event("metrics", payload))
				for _, metric := range []struct {
					name      string
					statistic string
				}{{"CPUUtilization", "Average"}, {"NetworkIn", "Sum"}, {"NetworkOut", "Sum"}, {"StatusCheckFailed", "Maximum"}} {
					queries = append(queries, MetricQuery{ID: "ec2_" + metric.name + "_" + instanceID, Namespace: "AWS/EC2", MetricName: metric.name, Statistic: metric.statistic, Period: 60, Dimensions: map[string]string{"InstanceId": instanceID}})
				}
			}
		}
		if output.NextToken == nil || aws.ToString(output.NextToken) == "" {
			break
		}
		input.NextToken = output.NextToken
	}
	return events, queries, nil
}

func availabilityZone(instance types.Instance) string {
	if instance.Placement == nil {
		return ""
	}
	return aws.ToString(instance.Placement.AvailabilityZone)
}

func (c *Collector) collectAuditEvents(ctx context.Context) ([]agent.Event, error) {
	start := time.Now().UTC().Add(-c.config.Lookback)
	var events []agent.Event
	input := &cloudtrail.LookupEventsInput{StartTime: &start, EndTime: awsTime(time.Now().UTC()), MaxResults: aws.Int32(50)}
	for {
		output, err := c.cloudtrail.LookupEvents(ctx, input)
		if err != nil {
			return nil, err
		}
		for _, audit := range output.Events {
			eventID := aws.ToString(audit.EventId)
			if eventID != "" {
				if _, seen := c.seenEvents[eventID]; seen {
					continue
				}
				c.seenEvents[eventID] = struct{}{}
			}
			payload := map[string]any{"source": "aws.cloudtrail", "region": c.config.Region, "event_id": eventID, "event_name": aws.ToString(audit.EventName), "username": aws.ToString(audit.Username), "event_time": audit.EventTime, "cloud_trail_event": aws.ToString(audit.CloudTrailEvent)}
			events = append(events, event("logs", payload))
		}
		if output.NextToken == nil || aws.ToString(output.NextToken) == "" {
			break
		}
		input.NextToken = output.NextToken
	}
	return events, nil
}

func (c *Collector) loadState() error {
	body, err := os.ReadFile(c.config.StatePath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read AWS collector state: %w", err)
	}
	var ids []string
	if err := json.Unmarshal(body, &ids); err != nil {
		return fmt.Errorf("decode AWS collector state: %w", err)
	}
	for _, id := range ids {
		c.seenEvents[id] = struct{}{}
	}
	return nil
}

func (c *Collector) saveState() error {
	ids := make([]string, 0, len(c.seenEvents))
	for id := range c.seenEvents {
		ids = append(ids, id)
	}
	body, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	if err := os.WriteFile(c.config.StatePath, body, 0600); err != nil {
		return fmt.Errorf("write AWS collector state: %w", err)
	}
	return nil
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
