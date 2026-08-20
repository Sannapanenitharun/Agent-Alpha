package awscollector

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	"github.com/signal-observability/collector/internal/agent"
)

func (c *Collector) collectTags(ctx context.Context) ([]agent.Event, error) {
	var events []agent.Event
	input := &resourcegroupstaggingapi.GetResourcesInput{}
	for {
		output, err := c.tags.GetResources(ctx, input)
		if err != nil {
			return nil, err
		}
		for _, resource := range output.ResourceTagMappingList {
			tags := map[string]string{}
			for _, tag := range resource.Tags {
				tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
			}
			events = append(events, event("metrics", map[string]any{"source": "aws.resource-groups-tagging", "region": c.config.Region, "account_id": c.config.AccountID, "resource_type": "aws_resource", "resource_arn": aws.ToString(resource.ResourceARN), "tags": tags}))
		}
		if output.PaginationToken == nil || aws.ToString(output.PaginationToken) == "" {
			break
		}
		input.PaginationToken = output.PaginationToken
	}
	return events, nil
}
