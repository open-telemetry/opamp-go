package certs

import (
	_ "embed"
)

//go:embed certs/ca.cert.pem
var CaCert []byte

//go:embed private/ca.key.pem
var CaKey []byte

//go:embed server_certs/server.cert.pem
var ServerCert []byte

//go:embed server_certs/server.key.pem
var ServerKey []byte
