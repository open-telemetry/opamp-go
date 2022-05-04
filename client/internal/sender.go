package internal

// Sender is an interface of the sending portion of OpAMP protocol that stores
// the NextMessage to be sent and can be ordered to send the message.
type Sender interface {
	// NextMessage gives access to the next message that will be sent by this Sender.
	// Can be called concurrently with any other method.
	NextMessage() *NextMessage

	// ScheduleSend signals to Sender that the message in NextMessage struct
	// is now ready to be sent.  The Sender should send the NextMessage as soon as possible.
	// If there is no pending message (e.g. the NextMessage was already sent and
	// "pending" flag is reset) then no message will be sent.
	ScheduleSend()

	// SetInstanceUid sets a new instanceUid to be used when NextMessage is being sent.
	SetInstanceUid(instanceUid string) error
}
