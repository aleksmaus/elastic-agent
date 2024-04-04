// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package configuration

import (
	"fmt"
)

// GRPCConfig is a configuration of GRPC server.
type GRPCConfig struct {
	Address                 string `config:"address"`
	Port                    int32  `config:"port"` // Using int32 instead of int16, since now it is signed and negative values mean local RPC
	MaxMsgSize              int    `config:"max_message_size"`
	CheckinChunkingDisabled bool   `config:"checkin_chunking_disabled"`
}

// DefaultGRPCConfig creates a default server configuration.
func DefaultGRPCConfig() *GRPCConfig {
	return &GRPCConfig{
		Address:                 "localhost",
		Port:                    -1,                // -1 (negative) port value by default enabled "local" rpc utilizing domain sockets and named pipes
		MaxMsgSize:              1024 * 1024 * 100, // grpc default 4MB is unsufficient for diagnostics
		CheckinChunkingDisabled: false,             // on by default
	}
}

// String returns the composed listen address for the GRPC.
func (cfg *GRPCConfig) String() string {
	return fmt.Sprintf("%s:%d", cfg.Address, cfg.Port)
}

// IsLocal returns true if port value is less than 0
func (cfg *GRPCConfig) IsLocal() bool {
	return cfg.Port < 0
}
