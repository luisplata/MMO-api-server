// Package protocol defines the wire-level protocol contract for the
// world simulation: the protobuf messages generated from proto/v1 and
// (from PR2 onward) the 10-byte envelope header and the type-id registry.
//
// PR1 scope: the committed protobuf bindings (proto/v1/gen/go) plus the
// tests that pin the contract — round-trip identity, unknown-field
// preservation across an additive minor bump, and the reflection-free /
// no-legacy-format guards.
package protocol
