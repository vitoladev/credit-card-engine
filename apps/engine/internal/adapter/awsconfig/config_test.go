package awsconfig_test

import (
	"testing"

	"engine/internal/adapter/awsconfig"
)

func TestLoadUsesTheConfiguredEndpoint(t *testing.T) {
	t.Setenv("AWS_ENDPOINT_URL", "http://floci:4566")
	cfg, err := awsconfig.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseEndpoint == nil || *cfg.BaseEndpoint != "http://floci:4566" {
		t.Fatalf("endpoint=%v", cfg.BaseEndpoint)
	}
}

func TestLoadOmitsTheEndpointWhenUnset(t *testing.T) {
	t.Setenv("AWS_ENDPOINT_URL", "")
	cfg, err := awsconfig.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseEndpoint != nil && *cfg.BaseEndpoint != "" {
		t.Fatalf("endpoint=%v", cfg.BaseEndpoint)
	}
}
