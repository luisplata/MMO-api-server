// Package protocol defines the wire-level protocol contract for the
// world simulation: the protobuf messages generated from proto/v1, the
// 11-byte envelope header (design D1) and the type-id registry (design
// D2).
//
// The committed protobuf bindings (proto/v1/gen/go) are the source of
// truth for message shapes. The envelope codec adds the fixed header
// [magic u16=0x4D4D][ver u16][type u16][flags u8][seq u32] (big-endian)
// and the registry dispatches the envelope type id to the matching
// protobuf message, with parity against the type-id table documented in
// proto/v1/world.proto enforced by test.
package protocol
