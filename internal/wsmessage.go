package internal

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"

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

// WebsocketConn is an abstraction of a websocket.Conn. It is useful for testing
// purposes.
type WebsocketConn interface {
	ReadMessage() (int, []byte, error)
	NextWriter(int) (io.WriteCloser, error)
	WriteControl(int, []byte, time.Time) error
	Close() error
	UnderlyingConn() net.Conn
}

// WriteWSMessage writes a proto.Message to a websocket.Conn. It returns the
// number of bytes written, and any error that occurs.
func WriteWSMessage(conn WebsocketConn, msg proto.Message) (int, error) {
	data, err := proto.Marshal(msg)
	if err != nil {
		return 0, err
	}

	writer, err := conn.NextWriter(websocket.BinaryMessage)
	if err != nil {
		return 0, err
	}

	// Encode header as a varint.
	hdrBuf := make([]byte, binary.MaxVarintLen64)
	n := binary.PutUvarint(hdrBuf, wsMsgHeader)
	hdrBuf = hdrBuf[:n]

	// Write the header bytes.
	hdrBytes, err := writer.Write(hdrBuf)
	if err != nil {
		writer.Close()
		return hdrBytes, err
	}

	// Write the encoded data.
	dataBytes, err := writer.Write(data)
	if err != nil {
		writer.Close()
		return dataBytes + hdrBytes, err
	}

	return hdrBytes + dataBytes, writer.Close()
}
