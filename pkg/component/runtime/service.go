// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package runtime

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"

	"github.com/kardianos/service"

	"github.com/elastic/elastic-agent-client/v7/pkg/client"
	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
	"github.com/elastic/elastic-agent/pkg/component"
	"github.com/elastic/elastic-agent/pkg/core/logger"
)

const (
	defaultCheckServiceStatusInterval = 30 * time.Second // 30 seconds default for now, consistent with the command check-in interval
	defaultServiceStopTimeout         = 3 * time.Minute  // 3 minutes default wait for the service stop
	defaultServiceStartTimeout        = time.Minute      // 1 minute default wait for the service start
)

var (
	ErrOperationSpecUndefined = errors.New("operation spec undefined")
	ErrInvalidServiceSpec     = errors.New("invalid service spec")
	ErrFailedServiceStop      = errors.New("failed service stop")
)

type platformServiceFunc func(name string) (service.Service, error)
type executeServiceCommandFunc func(ctx context.Context, log *logger.Logger, binaryPath string, spec *component.ServiceOperationsCommandSpec) error

// ServiceRuntime provides the command runtime for running a component as a service.
type ServiceRuntime struct {
	comp component.Component
	log  *logger.Logger

	ch       chan ComponentState
	actionCh chan actionMode
	compCh   chan component.Component
	statusCh chan service.Status

	state          ComponentState
	lastCheckin    time.Time
	missedCheckins int

	platformServiceImpl       platformServiceFunc
	executeServiceCommandImpl executeServiceCommandFunc
}

// NewServiceRuntime creates a new command runtime for the provided component.
func NewServiceRuntime(comp component.Component, logger *logger.Logger) (ComponentRuntime, error) {
	if comp.Spec.Spec.Service == nil {
		return nil, errors.New("must have service defined in specification")
	}

	state := newComponentState(&comp)

	s := &ServiceRuntime{
		comp:                      comp,
		log:                       logger.Named("service_runtime"),
		ch:                        make(chan ComponentState),
		actionCh:                  make(chan actionMode),
		compCh:                    make(chan component.Component),
		statusCh:                  make(chan service.Status),
		state:                     state,
		platformServiceImpl:       platformService,
		executeServiceCommandImpl: executeServiceCommand,
	}

	// Set initial state as STOPPED
	s.state.compState(client.UnitStateStopped, fmt.Sprintf("Stopped: %s service", s.serviceName()))
	return s, nil
}

// Run starts the runtime for the component.
//
// Called by Manager inside a goroutine. Run should not return until the passed in context is done. Run should always
// be called before any of the other methods in the interface and once the context is done none of those methods should
// ever be called again.
func (s *ServiceRuntime) Run(ctx context.Context, comm Communicator) (err error) {
	cli, err := s.platformService()
	if err != nil {
		return fmt.Errorf("failed create service client %s: %w", cli, err)
	}

	checkinTimer := time.NewTimer(s.checkinPeriod())
	defer checkinTimer.Stop()

	// Stop the check-ins timer initially
	checkinTimer.Stop()

	var (
		cis       *connInfoServer
		checkedIn bool
	)

	cisStop := func() {
		if cis != nil {
			_ = cis.stop()
			cis = nil
		}
	}
	defer cisStop()

	for {
		select {
		case <-ctx.Done():
			s.log.Debug("context is done. exiting.")
			return ctx.Err()
		case as := <-s.actionCh:
			var err error
			switch as {
			case actionStart:
				if !s.isRunning() {
					checkedIn = false
				}
				// Start connection info
				if cis == nil {
					cis, err = newConnInfoServer(s.log, comm, s.comp.Spec.Spec.Service.CPort)
					if err != nil {
						err = fmt.Errorf("failed to start connection info service %s: %w", cli, err)
					}
				}
				if err != nil {
					break
				}
				// Start service
				err = s.start(ctx, comm, cli)
				if err != nil {
					cisStop()
					break
				}
				// Start check-in timer
				checkinTimer.Reset(s.checkinPeriod())
			case actionStop, actionTeardown:
				// Stop check-in timer
				s.log.Debugf("stop check-in timer for %s service", s.serviceName())
				checkinTimer.Stop()

				// Stop connection info
				s.log.Debugf("stop connection info for %s service", s.serviceName())
				cisStop()

				// Stop service
				s.log.Debug("stop %s service", s.serviceName())
				err = s.stop(ctx, comm, cli, checkedIn)
				if err != nil {
					break
				}
				if as == actionTeardown {
					s.log.Debug("uninstall %s service", s.serviceName())
					err = s.uninstall(ctx)
				}
			}
			if err != nil {
				s.forceCompState(client.UnitStateFailed, err.Error())
			}
		case newComp := <-s.compCh:
			s.processNewComp(newComp, comm)
		case checkin := <-comm.CheckinObserved():
			checkedIn = true
			s.processCheckin(checkin, comm)
		case <-checkinTimer.C:
			s.checkStatus(cli, s.checkinPeriod())
		}
	}
}

func (s *ServiceRuntime) processNewComp(newComp component.Component, comm Communicator) {
	s.log.Debugf("observed component update for %s service", s.serviceName())
	sendExpected := s.state.syncExpected(&newComp)
	changed := s.state.syncUnits(&newComp)
	if sendExpected || s.state.unsettled() {
		comm.CheckinExpected(s.state.toCheckinExpected())
	}
	if changed {
		s.sendObserved()
	}
}

func (s *ServiceRuntime) processCheckin(checkin *proto.CheckinObserved, comm Communicator) {
	s.log.Debugf("observed check-in for %s service: %v", s.serviceName(), checkin)
	sendExpected := false
	changed := false

	// Derive the component state from the checkin units
	state := client.UnitStateHealthy
	msg := fmt.Sprintf("Healthy: communicating with %s service", s.serviceName())

	for _, unit := range checkin.Units {
		if int(unit.State) > int(state) {
			state = client.UnitState(unit.State)
			msg = unit.Message
		}
	}
	if s.state.State != state || s.state.Message != msg {
		s.state.State = state
		s.state.Message = msg
	}

	s.log.Debugf("current state: %v\n", s.state)

	isRunning := s.isRunning()
	if s.lastCheckin.IsZero() && isRunning {
		// first check-in
		sendExpected = true
	}
	s.lastCheckin = time.Now().UTC()
	if s.state.syncCheckin(checkin) {
		changed = true
	}
	if s.state.unsettled() && isRunning {
		sendExpected = true
	}
	if sendExpected {
		comm.CheckinExpected(s.state.toCheckinExpected())
	}
	if changed {
		s.sendObserved()
	}
	if s.state.cleanupStopped() {
		s.sendObserved()
	}
}

// isRunning returns true is the service is running
func (s *ServiceRuntime) isRunning() bool {
	return s.state.State == client.UnitStateHealthy ||
		s.state.State == client.UnitStateDegraded
}

// checkStatus checks check-ins state, called on timer
func (s *ServiceRuntime) checkStatus(cli service.Service, checkinPeriod time.Duration) {
	if s.isRunning() {
		now := time.Now().UTC()
		if s.lastCheckin.IsZero() {
			// never checked-in
			s.missedCheckins++
		} else if now.Sub(s.lastCheckin) > checkinPeriod {
			// missed check-in during required period
			s.missedCheckins++
		} else if now.Sub(s.lastCheckin) <= checkinPeriod {
			s.missedCheckins = 0
		}
		if s.missedCheckins == 0 {
			s.compState(client.UnitStateHealthy)
		} else if s.missedCheckins > 0 && s.missedCheckins < maxCheckinMisses {
			s.compState(client.UnitStateDegraded)
		} else if s.missedCheckins >= maxCheckinMisses {
			// something is wrong; the service should be checking in
			msg := fmt.Sprintf("Failed: %s service missed %d check-ins", cli, maxCheckinMisses)
			s.forceCompState(client.UnitStateFailed, msg)
		}
	}
}

func (s *ServiceRuntime) start(ctx context.Context, comm Communicator, cli service.Service) (err error) {
	name := s.serviceName()
	s.log.Debugf("start %s service", name)

	if s.state.State != client.UnitStateStopped {
		s.log.Debugf("%s service can't be started, already %s", name, s.state.State)
		return nil
	}

	// Set state to starting
	s.forceCompState(client.UnitStateStarting, fmt.Sprintf("Starting: %s service", name))

	// Reset check-ins tracking
	s.lastCheckin = time.Time{}
	s.missedCheckins = 0

	// Call the check command of the service
	err = s.check(ctx)
	if err != nil {
		// Check failed, call the install command of the service
		s.log.Debugf("failed check %s service: %v, try install", name, err)
		err = s.install(ctx)
		if err != nil {
			return fmt.Errorf("failed install %s service: %w", name, err)
		}
	}

	// Check service status if it's already running
	status, err := cli.Status()
	if err != nil {
		return fmt.Errorf("failed checking %s service status: %w", name, err)
	}

	if status != service.StatusRunning {
		// Start the service
		err = cli.Start()
		if err != nil {
			return fmt.Errorf("failed starting %s service: %w", name, err)
		}
	}
	return nil
}

func (s *ServiceRuntime) awaitCheckinState(ctx context.Context, comm Communicator, cli service.Service, state client.UnitState, timeout time.Duration) {
	t := time.NewTimer(s.stopTimeout())
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			// stop cancelled
			s.log.Debugf("stopping %s service, cancelled", cli)
			return
		case <-t.C:
			// stop timed out
			s.log.Debugf("stopping %s service, timed out", cli)
			return
		case checkin := <-comm.CheckinObserved():
			s.processCheckin(checkin, comm)
			// Return on the first matching unit state
			// This is used for the start/stop logic
			for _, unit := range s.state.Units {
				if unit.State == state {
					return
				}
			}
		}
	}
}

func (s *ServiceRuntime) awaitServiceStatus(ctx context.Context, status service.Status) (service.Status, error) {
	var (
		lastServiceStatus service.Status
	)

	name := s.serviceName()
	sw, err := newServiceWatcher(name)
	if err != nil {
		return lastServiceStatus, fmt.Errorf("failed to create the %s service watcher, err: %w", name, err)
	}

	// Run service watcher.
	ctx, cn := context.WithCancel(ctx)
	defer cn()
	go func() {
		sw.run(ctx)
	}()

	// Th sw.status() channel is closed on timeout, default 3 minutes
	for r := range sw.status() {
		if r.Err != nil {
			// If service watcher returned the error, log the error and exit the loop
			s.log.Errorf("%s service watcher returned err: %v", name, r.Err)
			return lastServiceStatus, r.Err
		} else {
			lastServiceStatus = r.Status
			switch r.Status {
			case service.StatusUnknown:
				s.log.Debugf("%s service watcher status: Unknown", name)
			case service.StatusRunning:
				s.log.Debugf("%s service watcher status: Running", name)
			case service.StatusStopped:
				s.log.Debugf("%s service watcher status: Stopped", name)
			}
			if status == r.Status {
				return lastServiceStatus, nil
			}
		}
	}

	return lastServiceStatus, nil
}

func (s *ServiceRuntime) setStopped(name string) {
	s.forceCompState(client.UnitStateStopped, fmt.Sprintf("Stopped: %s service", name))
	// Do the same as checkin handler, for the same state transition logic
	if s.state.cleanupStopped() {
		s.sendObserved()
	}
}

func (s *ServiceRuntime) stop(ctx context.Context, comm Communicator, cli service.Service, checkedIn bool) (err error) {
	s.log.Debugf("stopping %s service", cli)

	name := s.serviceName()
	if s.state.State == client.UnitStateStopping || s.state.State == client.UnitStateStopped {
		s.log.Debugf("%s service can't be stopped, already %s", name, s.state.State)
		return nil
	}

	// Send stopping state to the service
	if checkedIn {
		s.log.Debugf("send stopping state to %s service", name)
		s.state.forceExpectedState(client.UnitStateStopping)
		comm.CheckinExpected(s.state.toCheckinExpected())
		// Awating the service to check-in with stopping state
		s.log.Debugf("await check-in upon stopping for %s service", name)
		s.awaitCheckinState(ctx, comm, cli, client.UnitStateStopping, s.stopTimeout())
		s.log.Debugf("got check-in upon stopping for %s service", name)
		// Set state to stopped if service received STOPPING state
		// Presently the final state is STOPPING
		// The Endpoint will make changes to send STOPPED when the service is actually stopped
		if s.state.State == client.UnitStateStopping {
			s.setStopped(s.serviceName())
			return nil
		}
	}

	// Attempt to stop the service on non-windows platform
	if runtime.GOOS != "windows" {
		s.log.Debugf("attempt to stop %s service", name)
		err = cli.Stop()
		if err != nil {
			return err
		}

		// Await the service status stopped with the platform services management
		var status service.Status
		s.log.Debugf("wait for %s service to stop", name)
		status, err = s.awaitServiceStatus(ctx, service.StatusStopped)
		s.log.Debugf("got %s service [%s] status upon stop, err: %v", name, status, err)

		// If service is stopped or uninstalled, set the runtime state into stopped
		if err != nil {
			if !errors.Is(err, service.ErrNotInstalled) {
				return err
			}
		}
		if status == service.StatusStopped {
			s.setStopped(s.serviceName())
		} else {
			s.forceCompState(client.UnitStateFailed, fmt.Sprintf("Failed: while stopping %s service, status: %v", cli, status))
		}
	} else {
		s.setStopped(s.serviceName())
	}

	return nil
}

func (s *ServiceRuntime) checkinPeriod() time.Duration {
	checkinPeriod := s.comp.Spec.Spec.Service.Timeouts.Checkin
	if checkinPeriod == 0 {
		checkinPeriod = defaultCheckServiceStatusInterval
	}
	return checkinPeriod
}

func (s *ServiceRuntime) stopTimeout() time.Duration {
	stopTimeout := s.comp.Spec.Spec.Service.Timeouts.Stop
	if stopTimeout == 0 {
		stopTimeout = defaultServiceStopTimeout
	}
	return stopTimeout
}

// Watch returns a channel to watch for component state changes.
//
// A new state is sent anytime the state for a unit or the whole component changes.
func (s *ServiceRuntime) Watch() <-chan ComponentState {
	return s.ch
}

// Start starts the service.
//
// Non-blocking and never returns an error.
func (s *ServiceRuntime) Start() error {
	s.actionCh <- actionStart
	return nil
}

// Update updates the currComp runtime with a new-revision for the component definition.
//
// Non-blocking and never returns an error.
func (s *ServiceRuntime) Update(comp component.Component) error {
	s.compCh <- comp
	return nil
}

// Stop stops the service.
//
// Non-blocking and never returns an error.
func (s *ServiceRuntime) Stop() error {
	s.actionCh <- actionStop
	return nil
}

// Teardown stop and uninstall the service.
//
// Non-blocking and never returns an error.
func (s *ServiceRuntime) Teardown() error {
	s.actionCh <- actionTeardown
	return nil
}

func (s *ServiceRuntime) forceCompState(state client.UnitState, msg string) {
	if s.state.forceState(state, msg) {
		s.sendObserved()
	}
}

func (s *ServiceRuntime) sendObserved() {
	s.ch <- s.state.Copy()
}

func (s *ServiceRuntime) compState(state client.UnitState) {
	name := s.serviceName()
	msg := stateUnknownMessage
	if state == client.UnitStateHealthy {
		msg = fmt.Sprintf("Healthy: communicating with %s service", name)
	} else if state == client.UnitStateDegraded {
		if s.missedCheckins == 1 {
			msg = fmt.Sprintf("Degraded: %s service missed 1 check-in", name)
		} else {
			msg = fmt.Sprintf("Degraded: %s missed %d check-ins", name, s.missedCheckins)
		}
	}
	if s.state.compState(state, msg) {
		s.sendObserved()
	}
}

func (s *ServiceRuntime) serviceName() string {
	if s.comp.Spec.Spec.Service != nil {
		return s.comp.Spec.Spec.Service.Name
	}
	return ""
}

// platformService returns the service.Service client that allows to manage the lifecycle of the service
func (s *ServiceRuntime) platformService() (service.Service, error) {
	name := s.comp.Spec.Spec.Service.Name
	if name == "" {
		return nil, fmt.Errorf("missing service name: %w", ErrInvalidServiceSpec)
	}

	return s.platformServiceImpl(name)
}

func platformService(name string) (service.Service, error) {
	svcConfig := &service.Config{
		Name: name,
	}

	cli, err := service.New(nil, svcConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to service %s: %w", svcConfig.Name, err)
	}
	return cli, nil
}

// check executes the service check command
func (s *ServiceRuntime) check(ctx context.Context) error {
	if s.comp.Spec.Spec.Service.Operations.Check == nil {
		s.log.Errorf("missing check spec for %s service", s.comp.Spec.BinaryName)
		return ErrOperationSpecUndefined
	}
	s.log.Debugf("check if the %s is installed", s.comp.Spec.BinaryName)
	return s.executeServiceCommandImpl(ctx, s.log, s.comp.Spec.BinaryPath, s.comp.Spec.Spec.Service.Operations.Check)
}

// install executes the service install command
func (s *ServiceRuntime) install(ctx context.Context) error {
	if s.comp.Spec.Spec.Service.Operations.Install == nil {
		s.log.Errorf("missing install spec for %s service", s.comp.Spec.BinaryName)
		return ErrOperationSpecUndefined
	}
	s.log.Debugf("install %s service", s.comp.Spec.BinaryName)
	return s.executeServiceCommandImpl(ctx, s.log, s.comp.Spec.BinaryPath, s.comp.Spec.Spec.Service.Operations.Install)
}

// uninstall executes the service uninstall command
func (s *ServiceRuntime) uninstall(ctx context.Context) error {
	if s.comp.Spec.Spec.Service.Operations.Uninstall == nil {
		s.log.Errorf("missing uninstall spec for %s service", s.comp.Spec.BinaryName)
		return ErrOperationSpecUndefined
	}
	s.log.Debugf("uninstall %s service", s.comp.Spec.BinaryName)
	return s.executeServiceCommandImpl(ctx, s.log, s.comp.Spec.BinaryPath, s.comp.Spec.Spec.Service.Operations.Uninstall)
}
