// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

// Code generated by elastic-agent/dev-tools/cmd/buildspec/buildspec.go - DO NOT EDIT.

package program

import (
	"strings"

	"github.com/elastic/elastic-agent/pkg/packer"
)

var Supported []Spec
var SupportedMap map[string]Spec

func init() {
	// Packed Files
	// internal/spec/apm-server.yml
	// internal/spec/auditbeat.yml
	// internal/spec/cloudbeat.yml
	// internal/spec/endpoint.yml
	// internal/spec/filebeat.yml
	// internal/spec/fleet-server.yml
	// internal/spec/heartbeat.yml
	// internal/spec/metricbeat.yml
	// internal/spec/osquerybeat.yml
	// internal/spec/packetbeat.yml
	unpacked := packer.MustUnpack("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")
	SupportedMap = make(map[string]Spec)

	for f, v := range unpacked {
		s, err := NewSpecFromBytes(v)
		if err != nil {
			panic("Cannot read spec from " + f + ": " + err.Error())
		}
		Supported = append(Supported, s)
		SupportedMap[strings.ToLower(s.Cmd)] = s
	}
}
