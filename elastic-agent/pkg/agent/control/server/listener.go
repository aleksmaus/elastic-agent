// Licensed to Elasticsearch B.V. under one or more contributor
// license agreements. See the NOTICE file distributed with
// this work for additional information regarding copyright
// ownership. Elasticsearch B.V. licenses this file to you under
// the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

// +build !windows

package server

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/elastic/elastic-agent-poc/elastic-agent/pkg/agent/control"
	"github.com/elastic/elastic-agent-poc/elastic-agent/pkg/agent/errors"
	"github.com/elastic/elastic-agent-poc/elastic-agent/pkg/core/logger"
)

func createListener(log *logger.Logger) (net.Listener, error) {
	path := strings.TrimPrefix(control.Address(), "unix://")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		cleanupListener(log)
	}
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		err = os.MkdirAll(dir, 0755)
		if err != nil {
			return nil, err
		}
	}
	lis, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	err = os.Chmod(path, 0700)
	if err != nil {
		// failed to set permissions (close listener)
		lis.Close()
		return nil, err
	}
	return lis, err
}

func cleanupListener(log *logger.Logger) {
	path := strings.TrimPrefix(control.Address(), "unix://")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Debug("%s", errors.New(err, fmt.Sprintf("Failed to cleanup %s", path), errors.TypeFilesystem, errors.M("path", path)))
	}
}