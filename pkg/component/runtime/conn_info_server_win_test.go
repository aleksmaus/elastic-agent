// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

//go:build windows

package runtime

import (
	"net"

	"github.com/Microsoft/go-winio"
)

func dialLocal(address string) (net.Conn, error) {
	return winio.DialPipe(address, nil)
}
