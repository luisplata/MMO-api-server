// Package protocol type-id dispatch registry (design D2, spec R6).
//
// The registry maps envelope type ids (u16) to their protobuf message
// types so a frame's payload can be encoded/decoded by dispatch. The
// type-id table is documented in the proto/v1/world.proto header (ids
// 1–12) and parity is enforced by TestWorldRegistryCompleteness.
package protocol

import (
	"errors"
	"strconv"

	"google.golang.org/protobuf/proto"

	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// ErrUnknownType is returned when a type id has no registered message, or
// when a message has no registered type id (spec S6.2).
var ErrUnknownType = errors.New("protocol: unknown message type")

// Registry dispatches envelope type ids to protobuf messages.
type Registry struct {
	byID   map[uint16]proto.Message // type id -> prototype message
	byName map[string]uint16        // descriptor full name -> type id
}

// NewWorldRegistry returns the registry matching the contract type-id
// table of proto/v1/world.proto (design D2 order, ids 1–12).
func NewWorldRegistry() *Registry {
	r := &Registry{
		byID:   make(map[uint16]proto.Message),
		byName: make(map[string]uint16),
	}
	r.register(1, &mmov1.Hello{})
	r.register(2, &mmov1.ServerInfo{})
	r.register(3, &mmov1.AuthRequest{})
	r.register(4, &mmov1.AuthResponse{})
	r.register(5, &mmov1.EnterWorld{})
	r.register(6, &mmov1.WorldSnapshot{})
	r.register(7, &mmov1.MoveInput{})
	r.register(8, &mmov1.Snapshot{})
	r.register(9, &mmov1.VersionMismatch{})
	r.register(10, &mmov1.Ack{})
	r.register(11, &mmov1.SpawnEntity{})
	r.register(12, &mmov1.DespawnEntity{})
	return r
}

// register wires a prototype message to a type id. It panics on a nil
// message or a duplicate id/name — the world registry is fixed at
// construction time and a programming error must fail loudly.
func (r *Registry) register(id uint16, m proto.Message) {
	if m == nil {
		panic("protocol: register with nil message")
	}
	if _, dup := r.byID[id]; dup {
		panic("protocol: duplicate type id " + strconv.Itoa(int(id)))
	}
	name := string(m.ProtoReflect().Descriptor().FullName())
	if _, dup := r.byName[name]; dup {
		panic("protocol: duplicate message name " + name)
	}
	r.byID[id] = m
	r.byName[name] = id
}

// NewMessage returns a fresh zero-value instance of the message
// registered for id. It returns ErrUnknownType when id is unregistered
// (spec S6.2, decode direction).
func (r *Registry) NewMessage(id uint16) (proto.Message, error) {
	m, ok := r.byID[id]
	if !ok {
		return nil, ErrUnknownType
	}
	return m.ProtoReflect().New().Interface(), nil
}

// typeIDFor looks up the registered id for a message instance, returning
// ErrUnknownType when the message type is not in the registry (spec
// S6.2, encode direction).
func (r *Registry) typeIDFor(m proto.Message) (uint16, error) {
	if m == nil {
		return 0, ErrUnknownType
	}
	id, ok := r.byName[string(m.ProtoReflect().Descriptor().FullName())]
	if !ok {
		return 0, ErrUnknownType
	}
	return id, nil
}

// EncodeMessage serializes msg into a full wire frame: the registry
// supplies the envelope type id from the message's registered type, the
// caller supplies version/flags/seq via the template envelope. Unknown
// message types fail with ErrUnknownType.
func (r *Registry) EncodeMessage(m proto.Message, tmpl Envelope) ([]byte, error) {
	id, err := r.typeIDFor(m)
	if err != nil {
		return nil, err
	}
	payload, err := proto.Marshal(m)
	if err != nil {
		return nil, err
	}
	tmpl.Type = id
	tmpl.Payload = payload
	return tmpl.Encode()
}

// DecodeMessage turns a decoded envelope into a typed protobuf message by
// dispatching on env.Type. Unregistered type ids fail with
// ErrUnknownType (spec S6.2, decode direction).
func (r *Registry) DecodeMessage(env Envelope) (proto.Message, error) {
	m, ok := r.byID[env.Type]
	if !ok {
		return nil, ErrUnknownType
	}
	msg := m.ProtoReflect().New().Interface()
	if err := proto.Unmarshal(env.Payload, msg); err != nil {
		return nil, err
	}
	return msg, nil
}

// Len reports the number of registered message types.
func (r *Registry) Len() int {
	return len(r.byID)
}
