package awscollector

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/signal-observability/collector/internal/agent"
)

func (c *Collector) collectECS(ctx context.Context) ([]agent.Event, []MetricQuery, error) {
	var events []agent.Event
	var queries []MetricQuery
	clusters, err := c.ecs.ListClusters(ctx, &ecs.ListClustersInput{})
	if err != nil {
		return nil, nil, err
	}
	for _, clusterARN := range clusters.ClusterArns {
		clusterName := lastARNPart(clusterARN)
		services, err := c.ecs.ListServices(ctx, &ecs.ListServicesInput{Cluster: aws.String(clusterARN)})
		if err != nil {
			return nil, nil, err
		}
		if len(services.ServiceArns) == 0 {
			continue
		}
		described, err := c.ecs.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: aws.String(clusterARN), Services: services.ServiceArns})
		if err != nil {
			return nil, nil, err
		}
		for _, service := range described.Services {
			serviceName := aws.ToString(service.ServiceName)
			payload := map[string]any{"source": "aws.ecs", "region": c.config.Region, "resource_type": "ecs_service", "cluster": clusterName, "cluster_arn": clusterARN, "service": serviceName, "service_arn": aws.ToString(service.ServiceArn), "status": service.Status, "desired_count": service.DesiredCount, "running_count": service.RunningCount, "pending_count": service.PendingCount, "launch_type": service.LaunchType}
			events = append(events, event("metrics", payload))
			for _, metric := range []struct{ name, statistic string }{{"CPUUtilization", "Average"}, {"MemoryUtilization", "Average"}, {"RunningTaskCount", "Average"}, {"DesiredTaskCount", "Average"}} {
				queries = append(queries, MetricQuery{ID: fmt.Sprintf("ecs_%s_%s_%s", metric.name, clusterName, serviceName), Namespace: "AWS/ECS", MetricName: metric.name, Statistic: metric.statistic, Period: 60, Dimensions: map[string]string{"ClusterName": clusterName, "ServiceName": serviceName}})
			}
		}
	}
	return events, queries, nil
}

func lastARNPart(value string) string {
	for index := len(value) - 1; index >= 0; index-- {
		if value[index] == '/' || value[index] == ':' {
			return value[index+1:]
		}
	}
	return value
}
