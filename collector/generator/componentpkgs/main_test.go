package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPackageFromFuncName(t *testing.T) {
	tests := []struct {
		name   string
		fn     string
		expect string
	}{
		{
			name:   "closure in init",
			fn:     "github.com/grafana/alloy/internal/component/loki/source/file.init.0.func1",
			expect: "github.com/grafana/alloy/internal/component/loki/source/file",
		},
		{
			name:   "top-level function",
			fn:     "github.com/grafana/alloy/internal/component/prometheus/scrape.New",
			expect: "github.com/grafana/alloy/internal/component/prometheus/scrape",
		},
		{
			name:   "dots in the module path",
			fn:     "gopkg.in/yaml%2ev3.Unmarshal",
			expect: "gopkg.in/yaml.v3",
		},
		{
			name:   "no dot after the last slash",
			fn:     "example.com/pkg",
			expect: "example.com/pkg",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expect, packageFromFuncName(tt.fn))
		})
	}
}

func TestPackageOf(t *testing.T) {
	pkg, err := packageOf(packageOf)
	require.NoError(t, err)
	require.Equal(t, "github.com/grafana/alloy/otel_engine/generator/componentpkgs", pkg)
}
