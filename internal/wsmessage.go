package internal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

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

// EncodeWSMessage encodes msg into the OpAMP wire format (varint header + protobuf bytes)
// and writes it to w in a single Write call. It does not close w.
//
// Writing the header and payload together is important for writers that decide
// whether to compress based on the size of the first Write call (e.g.
// coder/websocket's per-message deflate writer): a separate 1-byte header
// write would prevent compression even for large payloads.
func EncodeWSMessage(w io.Writer, msg proto.Message) error {
	data, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	// Encode header as a varint (wsMsgHeader == 0, always 1 byte).
	hdrBuf := make([]byte, binary.MaxVarintLen64)
	n := binary.PutUvarint(hdrBuf, wsMsgHeader)

	// Combine header and payload into one slice so a single Write carries the
	// full message size.
	payload := make([]byte, 0, n+len(data))
	payload = append(payload, hdrBuf[:n]...)
	payload = append(payload, data...)

	if _, err = w.Write(payload); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	return nil
}

// WriteWSMessage writes msg to conn as a single binary WebSocket message.
// Used by the server-side implementation which retains github.com/gorilla/websocket.
func WriteWSMessage(conn *websocket.Conn, msg proto.Message) error {
	writer, err := conn.NextWriter(websocket.BinaryMessage)
	if err != nil {
		return fmt.Errorf("next writer: %w", err)
	}

	if err := EncodeWSMessage(writer, msg); err != nil {
		writer.Close()
		return err
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("close writer: %w", err)
	}
	return nil
}
