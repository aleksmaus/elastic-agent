// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.elastic.co/apm"

	"github.com/elastic/elastic-agent/internal/pkg/agent/errors"
	"github.com/elastic/elastic-agent/internal/pkg/fleetapi"
	"github.com/elastic/elastic-agent/internal/pkg/fleetapi/client"
	"github.com/elastic/elastic-agent/pkg/core/logger"
)

const fleetTimeFormat = "2006-01-02T15:04:05.99999-07:00"

type agentInfo interface {
	AgentID() string
}

// Acker is acker capable of acking action in fleet.
type Acker struct {
	log       *logger.Logger
	client    client.Sender
	agentInfo agentInfo
}

// NewAcker creates a new fleet acker.
func NewAcker(
	log *logger.Logger,
	agentInfo agentInfo,
	client client.Sender,
) (*Acker, error) {
	return &Acker{
		log:       log,
		client:    client,
		agentInfo: agentInfo,
	}, nil
}

// SetClient sets client to be used for http communication.
func (f *Acker) SetClient(c client.Sender) {
	f.client = c
}

// Ack acknowledges action.
func (f *Acker) Ack(ctx context.Context, action fleetapi.Action) (err error) {
	span, ctx := apm.StartSpan(ctx, "ack", "app.internal")
	defer func() {
		apm.CaptureError(ctx, err).Send()
		span.End()
	}()
	// checkin
	agentID := f.agentInfo.AgentID()
	cmd := fleetapi.NewAckCmd(f.agentInfo, f.client)
	req := &fleetapi.AckRequest{
		Events: []fleetapi.AckEvent{
			constructEvent(action, agentID),
		},
	}

	_, err = cmd.Execute(ctx, req)
	if err != nil {
		return errors.New(err, fmt.Sprintf("acknowledge action '%s' for elastic-agent '%s' failed", action.ID(), agentID), errors.TypeNetwork)
	}

	f.log.Debugf("action with id '%s' was just acknowledged", action.ID())

	return nil
}

// AckBatch acknowledges multiple actions at once.
func (f *Acker) AckBatch(ctx context.Context, actions []fleetapi.Action) (res *fleetapi.AckResponse, err error) {
	f.log.Debugf("fleet acker: ackbatch, actions: %#v", actions)
	span, ctx := apm.StartSpan(ctx, "ackBatch", "app.internal")
	defer func() {
		apm.CaptureError(ctx, err).Send()
		span.End()
	}()
	// checkin
	agentID := f.agentInfo.AgentID()
	events := make([]fleetapi.AckEvent, 0, len(actions))
	ids := make([]string, 0, len(actions))
	for _, action := range actions {
		events = append(events, constructEvent(action, agentID))
		ids = append(ids, action.ID())
	}

	f.log.Debugf("fleet acker: ackbatch, events: %#v", events)
	if len(events) == 0 {
		// no events to send (nothing to do)
		return &fleetapi.AckResponse{}, nil
	}

	cmd := fleetapi.NewAckCmd(f.agentInfo, f.client)
	req := &fleetapi.AckRequest{
		Events: events,
	}

	f.log.Debugf("%d actions with ids '%s' acknowledging", len(ids), strings.Join(ids, ","))

	res, err = cmd.Execute(ctx, req)
	if err != nil {
		return nil, errors.New(err, fmt.Sprintf("acknowledge %d actions '%v' for elastic-agent '%s' failed", len(actions), actions, agentID), errors.TypeNetwork)
	}
	return res, nil
}

// Commit commits ack actions.
func (f *Acker) Commit(ctx context.Context) error {
	return nil
}

func constructEvent(action fleetapi.Action, agentID string) fleetapi.AckEvent {
	ackev := fleetapi.AckEvent{
		EventType: "ACTION_RESULT",
		SubType:   "ACKNOWLEDGED",
		Timestamp: time.Now().Format(fleetTimeFormat),
		ActionID:  action.ID(),
		AgentID:   agentID,
		Message:   fmt.Sprintf("Action '%s' of type '%s' acknowledged.", action.ID(), action.Type()),
	}

	if a, ok := action.(fleetapi.RetryableAction); ok {
		if err := a.GetError(); err != nil {
			ackev.Error = err.Error()
			var payload struct {
				Retry   bool `json:"retry"`
				Attempt int  `json:"retry_attempt,omitempty"`
			}
			payload.Retry = true
			payload.Attempt = a.RetryAttempt()
			if a.RetryAttempt() < 1 {
				payload.Retry = false
			}
			p, _ := json.Marshal(payload)
			ackev.Payload = p
		}
	}

	if a, ok := action.(*fleetapi.ActionApp); ok {
		ackev.ActionInputType = a.InputType
		ackev.ActionData = a.Data
		ackev.ActionResponse = a.Response
		ackev.StartedAt = a.StartedAt
		ackev.CompletedAt = a.CompletedAt
		ackev.Error = a.Error
	}
	return ackev
}
