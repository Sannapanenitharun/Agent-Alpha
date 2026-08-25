// Package telemetry holds the wire contract shared by the collector agent, the
// intake gateway, and the AWS collector. It lives in its own package so the
// backend services do not have to import the agent just to name the types they
// exchange with it.
package telemetry

import (
	"encoding/json"
	"time"
)

// Event is a single unit of telemetry travelling towards intake. Payload holds
// the OTLP JSON representation of the signal.
type Event struct {
	Type      string          `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

// Envelope is a batch of events attributed to one tenant.
//
// TenantID here is a claim made by the sender. The intake gateway must resolve
// the real tenant from the presented credential and reject any envelope that
// disagrees; it must never treat this field as proof of identity.
type Envelope struct {
	TenantID string  `json:"tenant_id"`
	Events   []Event `json:"events"`
}

// SignalTypes are the signal kinds intake accepts.
var SignalTypes = map[string]struct{}{
	"logs":    {},
	"metrics": {},
	"traces":  {},
}

// ValidSignal reports whether kind is a supported signal type.
func ValidSignal(kind string) bool {
	_, ok := SignalTypes[kind]
	return ok
}
