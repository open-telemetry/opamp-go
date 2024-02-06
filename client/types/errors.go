package types

import "errors"

var (
	// ErrCustomMessageMissing is returned by SetCustomMessage when called with a nil message.
	ErrCustomMessageMissing = errors.New("CustomMessage is nil")

	// ErrCustomCapabilityNotSupported is returned by SetCustomMessage when called with
	// message that has a capability that is not specified as supported by the client.
	ErrCustomCapabilityNotSupported = errors.New("CustomCapability of CustomMessage is not supported")

	// ErrCustomMessagePending is returned by SetCustomMessage when called before the previous
	// message has been sent.
	ErrCustomMessagePending = errors.New("custom message already set")
)
