package internal

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

// Message header is currently uint64 zero value.
const wsMsgHeader = uint64(0)

// DecodeWSMessage decodes a websocket message as bytes into a proto.Message.
func DecodeWSMessage(bytes []byte, msg proto.Message) error {
	// Message header is optional until the end of grace period that ends Feb 1, 2023.
	// Check if the header is present.
	if len(bytes) > 0 && bytes[0] == 0 {
		// New message format. The Protobuf message is preceded by a zero byte header.
		// Decode the header.
		header, n := binary.Uvarint(bytes)
		if header != wsMsgHeader {
			return errors.New("unexpected non-zero header")
		}
		// Skip the header. It really is just a single zero byte for now.
		bytes = bytes[n:]
	}
	// If no header was present (the "if" check above), then this is the old
	// message format. No header is present.

	// Decode WebSocket message as a Protobuf message.
	err := proto.Unmarshal(bytes, msg)
	if err != nil {
		return err
	}
	return nil
}

// WriteWSMessage encodes msg and writes it to conn as a single binary
// WebSocket message. On success it returns the number of bytes of the encoded
// message (header plus Protobuf payload), not including any WebSocket framing
// or compression.
func WriteWSMessage(conn *websocket.Conn, msg proto.Message, maxMessageSize int64) (int, error) {
	data, err := proto.Marshal(msg)
	if err != nil {
		return 0, fmt.Errorf("marshal message: %w", err)
	}

	// Encode header as a varint.
	hdrBuf := make([]byte, binary.MaxVarintLen64)
	n := binary.PutUvarint(hdrBuf, wsMsgHeader)
	hdrBuf = hdrBuf[:n]

	if err := CheckSizeLimit(int64(len(hdrBuf)+len(data)), maxMessageSize, "websocket message"); err != nil {
		return 0, err
	}

	writer, err := conn.NextWriter(websocket.BinaryMessage)
	if err != nil {
		return 0, fmt.Errorf("next writer: %w", err)
	}

	// Write the header bytes.
	_, err = writer.Write(hdrBuf)
	if err != nil {
		writer.Close()
		return 0, fmt.Errorf("write header: %w", err)
	}

	// Write the encoded data.
	_, err = writer.Write(data)
	if err != nil {
		writer.Close()
		return 0, fmt.Errorf("write data: %w", err)
	}

	err = writer.Close()
	if err != nil {
		return 0, fmt.Errorf("close writer: %w", err)
	}
	return len(hdrBuf) + len(data), nil
}
