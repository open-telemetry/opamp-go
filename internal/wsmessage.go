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

// StripWSMessageHeader removes the optional varint header from a
// WebSocket message and returns the protobuf payload bytes ready for
// proto.Unmarshal. The header is currently always uint64(0). When the
// header is absent (pre-2023 grace-period message format), the input
// is returned unchanged.
//
// This helper exists so that callers that need to choose a proto type
// at runtime (for example, the OpAMP client unwrapping a
// SignedServerToAgent envelope vs. a plain ServerToAgent) can strip
// the framing first and then unmarshal into the appropriate message
// type.
func StripWSMessageHeader(bytes []byte) ([]byte, error) {
	if len(bytes) > 0 && bytes[0] == 0 {
		header, n := binary.Uvarint(bytes)
		if header != wsMsgHeader {
			return nil, errors.New("unexpected non-zero header")
		}
		return bytes[n:], nil
	}
	return bytes, nil
}

// DecodeWSMessage decodes a websocket message as bytes into a proto.Message.
func DecodeWSMessage(bytes []byte, msg proto.Message) error {
	protoBytes, err := StripWSMessageHeader(bytes)
	if err != nil {
		return err
	}
	return proto.Unmarshal(protoBytes, msg)
}

func WriteWSMessage(conn *websocket.Conn, msg proto.Message, maxMessageSize int64) error {
	data, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	// Encode header as a varint.
	hdrBuf := make([]byte, binary.MaxVarintLen64)
	n := binary.PutUvarint(hdrBuf, wsMsgHeader)
	hdrBuf = hdrBuf[:n]

	if err := CheckSizeLimit(int64(len(hdrBuf)+len(data)), maxMessageSize, "websocket message"); err != nil {
		return err
	}

	writer, err := conn.NextWriter(websocket.BinaryMessage)
	if err != nil {
		return fmt.Errorf("next writer: %w", err)
	}

	// Write the header bytes.
	_, err = writer.Write(hdrBuf)
	if err != nil {
		writer.Close()
		return fmt.Errorf("write header: %w", err)
	}

	// Write the encoded data.
	_, err = writer.Write(data)
	if err != nil {
		writer.Close()
		return fmt.Errorf("write data: %w", err)
	}

	err = writer.Close()
	if err != nil {
		return fmt.Errorf("close writer: %w", err)
	}
	return nil
}
