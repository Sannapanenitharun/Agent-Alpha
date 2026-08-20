package awscollector

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadServiceConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aws.yaml")
	content := []byte("version: 1\nprovider: aws\ncollection:\n  regions: [us-west-2]\n  poll_interval: 45s\nservices:\n  compute: [ec2]\n")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadServiceConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Collection.Regions) != 1 || config.Collection.Regions[0] != "us-west-2" {
		t.Fatalf("unexpected regions: %+v", config.Collection.Regions)
	}
	if config.Collection.PollInterval != 45*time.Second {
		t.Fatalf("unexpected poll interval: %s", config.Collection.PollInterval)
	}
}
