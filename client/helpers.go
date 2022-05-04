package client

import (
	"crypto/sha256"
	"hash"

	"github.com/open-telemetry/opamp-go/protobufs"
	"google.golang.org/protobuf/encoding/protojson"
)

func writeHashKV(hash hash.Hash, kv *protobufs.KeyValue) error {
	// To keep the implementation simple we convert the data to an equivalent JSON
	// string and calculate the hash from the string bytes.
	b, err := protojson.Marshal(kv)
	if err != nil {
		return err
	}
	hash.Write(b)
	return nil
}

func writeHashKVList(hash hash.Hash, kvl []*protobufs.KeyValue) error {
	for _, kv := range kvl {
		err := writeHashKV(hash, kv)
		if err != nil {
			return err
		}
	}
	return nil
}

// CalcHashAgentDescription calculates and sets the Hash field from the rest of the
// fields in the message.
func CalcHashAgentDescription(msg *protobufs.AgentDescription) error {
	h := sha256.New()
	err := writeHashKVList(h, msg.IdentifyingAttributes)
	if err != nil {
		return err
	}
	err = writeHashKVList(h, msg.NonIdentifyingAttributes)
	if err != nil {
		return err
	}
	msg.Hash = h.Sum(nil)
	return nil
}

// CalcHashEffectiveConfig calculates and sets the Hash field from the rest of the
// fields in the message.
func CalcHashEffectiveConfig(msg *protobufs.EffectiveConfig) {
	h := sha256.New()
	if msg.ConfigMap != nil {
		for k, v := range msg.ConfigMap.ConfigMap {
			h.Write([]byte(k))
			h.Write(v.Body)
			h.Write([]byte(v.ContentType))
		}
	}
	msg.Hash = h.Sum(nil)
}
