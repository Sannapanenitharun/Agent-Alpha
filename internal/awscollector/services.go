package awscollector

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/signal-observability/collector/internal/agent"
)

type LambdaAPI interface {
	ListFunctions(context.Context, *lambda.ListFunctionsInput, ...func(*lambda.Options)) (*lambda.ListFunctionsOutput, error)
}
type RDSAPI interface {
	DescribeDBInstances(context.Context, *rds.DescribeDBInstancesInput, ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error)
}
type DynamoDBAPI interface {
	ListTables(context.Context, *dynamodb.ListTablesInput, ...func(*dynamodb.Options)) (*dynamodb.ListTablesOutput, error)
}
type SQSAPI interface {
	ListQueues(context.Context, *sqs.ListQueuesInput, ...func(*sqs.Options)) (*sqs.ListQueuesOutput, error)
}
type SNSAPI interface {
	ListTopics(context.Context, *sns.ListTopicsInput, ...func(*sns.Options)) (*sns.ListTopicsOutput, error)
}
type ELBAPI interface {
	DescribeLoadBalancers(context.Context, *elasticloadbalancingv2.DescribeLoadBalancersInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error)
}
type APIGatewayAPI interface {
	GetApis(context.Context, *apigatewayv2.GetApisInput, ...func(*apigatewayv2.Options)) (*apigatewayv2.GetApisOutput, error)
}
type EKSAPI interface {
	ListClusters(context.Context, *eks.ListClustersInput, ...func(*eks.Options)) (*eks.ListClustersOutput, error)
}
type CloudFrontAPI interface {
	ListDistributions(context.Context, *cloudfront.ListDistributionsInput, ...func(*cloudfront.Options)) (*cloudfront.ListDistributionsOutput, error)
}

type Services struct {
	Lambda     LambdaAPI
	RDS        RDSAPI
	DynamoDB   DynamoDBAPI
	SQS        SQSAPI
	SNS        SNSAPI
	ELB        ELBAPI
	APIGateway APIGatewayAPI
	CloudFront CloudFrontAPI
	EKS        EKSAPI
}

func (c *Collector) collectServices(ctx context.Context, services *Services) ([]agent.Event, []MetricQuery, error) {
	if services == nil {
		return nil, nil, nil
	}
	var events []agent.Event
	var queries []MetricQuery
	adapters := []struct {
		name    string
		enabled bool
		collect func() ([]agent.Event, []MetricQuery, error)
	}{
		{"lambda", services.Lambda != nil, func() ([]agent.Event, []MetricQuery, error) { return c.collectLambda(ctx, services.Lambda) }},
		{"rds", services.RDS != nil, func() ([]agent.Event, []MetricQuery, error) { return c.collectRDS(ctx, services.RDS) }},
		{"dynamodb", services.DynamoDB != nil, func() ([]agent.Event, []MetricQuery, error) { return c.collectDynamoDB(ctx, services.DynamoDB) }},
		{"sqs", services.SQS != nil, func() ([]agent.Event, []MetricQuery, error) { return c.collectSQS(ctx, services.SQS) }},
		{"sns", services.SNS != nil, func() ([]agent.Event, []MetricQuery, error) { return c.collectSNS(ctx, services.SNS) }},
		{"load_balancers", services.ELB != nil, func() ([]agent.Event, []MetricQuery, error) { return c.collectELB(ctx, services.ELB) }},
		{"api_gateway", services.APIGateway != nil, func() ([]agent.Event, []MetricQuery, error) { return c.collectAPIGateway(ctx, services.APIGateway) }},
		{"cloudfront", services.CloudFront != nil, func() ([]agent.Event, []MetricQuery, error) { return c.collectCloudFront(ctx, services.CloudFront) }},
		{"eks", services.EKS != nil, func() ([]agent.Event, []MetricQuery, error) { return c.collectEKS(ctx, services.EKS) }},
	}
	for _, adapter := range adapters {
		if !adapter.enabled {
			continue
		}
		gotEvents, gotQueries, err := adapter.collect()
		if err != nil {
			return nil, nil, fmt.Errorf("collect %s: %w", adapter.name, err)
		}
		events = append(events, gotEvents...)
		queries = append(queries, gotQueries...)
	}
	return events, queries, nil
}

func serviceQueries(namespace, prefix, resource string, names ...string) []MetricQuery {
	queries := make([]MetricQuery, 0, len(names))
	for _, name := range names {
		queries = append(queries, MetricQuery{ID: prefix + name + resource, Namespace: namespace, MetricName: name, Statistic: "Average", Period: 60})
	}
	return queries
}

func (c *Collector) collectLambda(ctx context.Context, api LambdaAPI) ([]agent.Event, []MetricQuery, error) {
	out, err := api.ListFunctions(ctx, &lambda.ListFunctionsInput{})
	if err != nil {
		return nil, nil, err
	}
	var events []agent.Event
	var queries []MetricQuery
	for _, fn := range out.Functions {
		name := aws.ToString(fn.FunctionName)
		events = append(events, event("metrics", map[string]any{"source": "aws.lambda", "region": c.config.Region, "resource_type": "lambda_function", "function": name, "runtime": fn.Runtime, "memory": fn.MemorySize}))
		for _, q := range serviceQueries("AWS/Lambda", "lambda_", name, "Invocations", "Errors", "Duration", "Throttles") {
			q.Dimensions = map[string]string{"FunctionName": name}
			queries = append(queries, q)
		}
	}
	return events, queries, nil
}

func (c *Collector) collectRDS(ctx context.Context, api RDSAPI) ([]agent.Event, []MetricQuery, error) {
	out, err := api.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{})
	if err != nil {
		return nil, nil, err
	}
	var events []agent.Event
	var queries []MetricQuery
	for _, db := range out.DBInstances {
		id := aws.ToString(db.DBInstanceIdentifier)
		events = append(events, event("metrics", map[string]any{"source": "aws.rds", "region": c.config.Region, "resource_type": "rds_instance", "db_instance": id, "engine": db.Engine, "status": db.DBInstanceStatus, "class": db.DBInstanceClass}))
		for _, q := range serviceQueries("AWS/RDS", "rds_", id, "CPUUtilization", "DatabaseConnections", "FreeStorageSpace", "ReadLatency", "WriteLatency") {
			q.Dimensions = map[string]string{"DBInstanceIdentifier": id}
			queries = append(queries, q)
		}
	}
	return events, queries, nil
}

func (c *Collector) collectDynamoDB(ctx context.Context, api DynamoDBAPI) ([]agent.Event, []MetricQuery, error) {
	out, err := api.ListTables(ctx, &dynamodb.ListTablesInput{})
	if err != nil {
		return nil, nil, err
	}
	var events []agent.Event
	var queries []MetricQuery
	for _, name := range out.TableNames {
		events = append(events, event("metrics", map[string]any{"source": "aws.dynamodb", "region": c.config.Region, "resource_type": "dynamodb_table", "table": name}))
		for _, q := range serviceQueries("AWS/DynamoDB", "dynamodb_", name, "ConsumedReadCapacityUnits", "ConsumedWriteCapacityUnits", "ReadThrottleEvents", "WriteThrottleEvents") {
			q.Dimensions = map[string]string{"TableName": name}
			queries = append(queries, q)
		}
	}
	return events, queries, nil
}

func (c *Collector) collectSQS(ctx context.Context, api SQSAPI) ([]agent.Event, []MetricQuery, error) {
	out, err := api.ListQueues(ctx, &sqs.ListQueuesInput{})
	if err != nil {
		return nil, nil, err
	}
	var events []agent.Event
	var queries []MetricQuery
	for _, url := range out.QueueUrls {
		name := lastARNPart(url)
		events = append(events, event("metrics", map[string]any{"source": "aws.sqs", "region": c.config.Region, "resource_type": "sqs_queue", "queue": name, "url": url}))
		for _, q := range serviceQueries("AWS/SQS", "sqs_", name, "NumberOfMessagesSent", "NumberOfMessagesReceived", "ApproximateNumberOfMessagesVisible", "NumberOfMessagesDeleted") {
			q.Dimensions = map[string]string{"QueueName": name}
			queries = append(queries, q)
		}
	}
	return events, queries, nil
}

func (c *Collector) collectSNS(ctx context.Context, api SNSAPI) ([]agent.Event, []MetricQuery, error) {
	out, err := api.ListTopics(ctx, &sns.ListTopicsInput{})
	if err != nil {
		return nil, nil, err
	}
	var events []agent.Event
	var queries []MetricQuery
	for _, topic := range out.Topics {
		arn := aws.ToString(topic.TopicArn)
		name := lastARNPart(arn)
		events = append(events, event("metrics", map[string]any{"source": "aws.sns", "region": c.config.Region, "resource_type": "sns_topic", "topic": name, "arn": arn}))
		for _, q := range serviceQueries("AWS/SNS", "sns_", name, "NumberOfMessagesPublished", "NumberOfNotificationsDelivered", "NumberOfNotificationsFailed") {
			q.Dimensions = map[string]string{"TopicName": name}
			queries = append(queries, q)
		}
	}
	return events, queries, nil
}

func (c *Collector) collectELB(ctx context.Context, api ELBAPI) ([]agent.Event, []MetricQuery, error) {
	out, err := api.DescribeLoadBalancers(ctx, &elasticloadbalancingv2.DescribeLoadBalancersInput{})
	if err != nil {
		return nil, nil, err
	}
	var events []agent.Event
	var queries []MetricQuery
	for _, lb := range out.LoadBalancers {
		arn := aws.ToString(lb.LoadBalancerArn)
		name := lastARNPart(arn)
		ns := "AWS/ApplicationELB"
		if lb.Type != "application" {
			ns = "AWS/NetworkELB"
		}
		events = append(events, event("metrics", map[string]any{"source": "aws.elb", "region": c.config.Region, "resource_type": "load_balancer", "name": name, "arn": arn, "state": lb.State, "type": lb.Type, "dns_name": lb.DNSName}))
		for _, q := range serviceQueries(ns, "elb_", name, "RequestCount", "HTTPCode_Target_5XX_Count", "TargetResponseTime", "HealthyHostCount", "UnHealthyHostCount") {
			q.Dimensions = map[string]string{"LoadBalancer": strings.TrimPrefix(name, "app/")}
			queries = append(queries, q)
		}
	}
	return events, queries, nil
}

func (c *Collector) collectAPIGateway(ctx context.Context, api APIGatewayAPI) ([]agent.Event, []MetricQuery, error) {
	out, err := api.GetApis(ctx, &apigatewayv2.GetApisInput{})
	if err != nil {
		return nil, nil, err
	}
	var events []agent.Event
	var queries []MetricQuery
	for _, item := range out.Items {
		id := aws.ToString(item.ApiId)
		events = append(events, event("metrics", map[string]any{"source": "aws.apigateway", "region": c.config.Region, "resource_type": "api_gateway", "api_id": id, "name": item.Name, "protocol": item.ProtocolType, "state": item.ApiEndpoint}))
		for _, q := range serviceQueries("AWS/ApiGateway", "apigateway_", id, "Count", "4XXError", "5XXError", "Latency", "IntegrationLatency") {
			q.Dimensions = map[string]string{"ApiId": id}
			queries = append(queries, q)
		}
	}
	return events, queries, nil
}

func (c *Collector) collectCloudFront(ctx context.Context, api CloudFrontAPI) ([]agent.Event, []MetricQuery, error) {
	out, err := api.ListDistributions(ctx, &cloudfront.ListDistributionsInput{})
	if err != nil {
		return nil, nil, err
	}
	var events []agent.Event
	var queries []MetricQuery
	if out.DistributionList == nil {
		return events, queries, nil
	}
	for _, dist := range out.DistributionList.Items {
		id := aws.ToString(dist.Id)
		events = append(events, event("metrics", map[string]any{"source": "aws.cloudfront", "region": "global", "resource_type": "cloudfront_distribution", "distribution": id, "domain": dist.DomainName, "status": dist.Status}))
		for _, q := range serviceQueries("AWS/CloudFront", "cloudfront_", id, "Requests", "BytesDownloaded", "4xxErrorRate", "5xxErrorRate", "TotalErrorRate") {
			q.Dimensions = map[string]string{"DistributionId": id, "Region": "Global"}
			queries = append(queries, q)
		}
	}
	return events, queries, nil
}

func (c *Collector) collectEKS(ctx context.Context, api EKSAPI) ([]agent.Event, []MetricQuery, error) {
	out, err := api.ListClusters(ctx, &eks.ListClustersInput{})
	if err != nil {
		return nil, nil, err
	}
	var events []agent.Event
	var queries []MetricQuery
	for _, name := range out.Clusters {
		events = append(events, event("metrics", map[string]any{"source": "aws.eks", "region": c.config.Region, "resource_type": "eks_cluster", "cluster": name}))
		for _, q := range serviceQueries("ContainerInsights", "eks_", name, "cluster_node_count", "cluster_failed_node_count", "node_cpu_utilization", "node_memory_utilization") {
			q.Dimensions = map[string]string{"ClusterName": name}
			queries = append(queries, q)
		}
	}
	return events, queries, nil
}
