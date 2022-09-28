// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package runtime

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dolmen-go/contextio"

	"github.com/elastic/elastic-agent/pkg/component"
	"github.com/elastic/elastic-agent/pkg/core/logger"
	"github.com/elastic/elastic-agent/pkg/core/process"
)

func executeCommand(ctx context.Context, log *logger.Logger, binaryPath string, args []string, env []string, timeout time.Duration) error {
	log = log.With("context", "command output")
	// Create context with timeout if the timeout is greater than 0
	if timeout > 0 {
		var cn context.CancelFunc
		ctx, cn = context.WithTimeout(ctx, timeout)
		defer cn()
	}

	opts := []process.StartOption{
		process.WithContext(ctx),
		process.WithArgs(args),
		process.WithEnv(env),
	}

	// Set the command working directory from binary
	// This is needed because the endpoint installer was looking for it's resources in the current working directory
	wdir := filepath.Dir(binaryPath)
	if wdir != "." {
		opts = append(opts,
			process.WithCmdOptions(func(c *exec.Cmd) error {
				c.Dir = wdir
				return nil
			}))
	}

	proc, err := process.Start(binaryPath, opts...)
	if err != nil {
		return fmt.Errorf("failed starting the command: %w", err)
	}

	// channel for the last error message from the stderr output
	errch := make(chan string, 1)
	ctxstderr := contextio.NewReader(ctx, proc.Stderr)
	if ctxstderr != nil {
		go func() {
			var errtext string
			scanner := bufio.NewScanner(ctxstderr)
			for scanner.Scan() {
				line := scanner.Bytes()
				if len(line) > 0 {
					txt := strings.TrimSpace(string(line))
					if len(txt) > 0 {
						errtext = strings.TrimSpace(string(line))
						// Log error output line
						log.Error(errtext)
					}
				}
			}
			errch <- errtext
		}()
	}

	procState := <-proc.Wait()
	if procState.ExitCode() == -1 && ctx.Err() != nil {
		err = ctx.Err() // Process was killed due to timeout
	} else if !procState.Success() {
		err = &exec.ExitError{ProcessState: procState}
	}

	if err != nil {
		errmsg := <-errch
		errmsg = strings.TrimSpace(errmsg)
		if errmsg != "" {
			err = fmt.Errorf("%s: %w", errmsg, err)
		}
	}

	return err
}

func executeServiceCommand(ctx context.Context, log *logger.Logger, binaryPath string, spec *component.ServiceOperationsCommandSpec) error {
	if spec == nil {
		return nil
	}
	return executeCommand(ctx, log, binaryPath, spec.Args, envSpecToEnv(spec.Env), spec.Timeout)
}

func envSpecToEnv(envSpecs []component.CommandEnvSpec) []string {
	if len(envSpecs) == 0 {
		return nil
	}

	env := make([]string, len(envSpecs))

	for i, spec := range envSpecs {
		env[i] = spec.Name + "=" + spec.Value
	}
	return env
}
