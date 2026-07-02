// policysrv is a minimal example of an out-of-process OpAMP policy/signing
// server, as described in supplementary-guidelines.md.
//
// # Architecture
//
// The OpAMP distribution server (internal/examples/server) holds no private
// key material. Before delivering each ServerToAgent message it calls this
// server's /v1/sign endpoint to obtain a signature over the payload bytes.
// The structural isolation is the key security property: an attacker who
// compromises the distribution server gains the ability to send messages,
// but cannot produce valid signatures without also compromising this server.
//
//	Agent ──AgentToServer──► OpAMP Server ──sign request──► Policy Server
//	                                       ◄──signature──────────────────
//	     ◄──SignedServerToAgent───────────
//
// # Policy enforcement
//
// In this example the server signs every request unconditionally. A
// production policy server would decode the ServerToAgent payload before
// signing and reject messages that violate organizational constraints, for
// example:
//   - Deny ServerToAgentCommand messages to immutable agents.
//   - Enforce per-team RemoteConfig ownership.
//   - Require that only the latest approved component version may be installed.
//
// # Usage (three separate terminals, all run from internal/examples/)
//
//	# Terminal 1 – policy/signing server
//	go run ./policysrv
//
//	# Terminal 2 – OpAMP distribution server (points at policy server)
//	go run ./server --policy-server http://localhost:4322
//
//	# Terminal 3 – agent (pre-provisioned with the CA cert written above)
//	go run ./agent --attestation-ca /tmp/opamp-policy-ca.pem
//
// In production the CA certificate would be distributed out-of-band via
// configuration management tooling (Ansible, Chef, Puppet, or a secrets
// manager), or compiled into the agent binary. The /v1/ca endpoint is
// provided for demo convenience only and is not part of the OpAMP protocol.
package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/open-telemetry/opamp-go/signing"
)

const (
	listenAddr = ":4322"
	caOutPath  = "/tmp/opamp-policy-ca.pem"
)

func main() {
	// Generate an ephemeral CA and signing leaf.
	// In production: load the CA and leaf private key from an HSM or
	// secrets manager; never write the private key to disk.
	ca, caKey, err := signing.GenerateCA(signing.AlgorithmECDSAP256SHA256, signing.CertOptions{})
	if err != nil {
		log.Fatalf("generate CA: %v", err)
	}
	leaf, leafKey, err := signing.GenerateLeaf(signing.AlgorithmECDSAP256SHA256, ca, caKey, signing.CertOptions{
		// SAN required by the spec: the leaf must match the OpAMP distribution server's hostname.
		// The example server binds to 0.0.0.0:4320; agents may connect by hostname or IP, so
		// include both. Production deployments set these to the actual hostname(s) or IP(s).
		DNSNames:    []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	})
	if err != nil {
		log.Fatalf("generate leaf: %v", err)
	}
	localSigner, err := signing.NewLocalSigner(leafKey, []*x509.Certificate{leaf})
	if err != nil {
		log.Fatalf("new signer: %v", err)
	}
	signer := localSigner.WithRootCA(ca)

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})
	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})

	// Write the CA cert for agents to load as their payload trust anchor.
	// In production this step is replaced by your configuration-management
	// pipeline; do not derive trust anchors from the network at runtime.
	if err := os.WriteFile(caOutPath, caPEM, 0o644); err != nil {
		log.Fatalf("write CA cert: %v", err)
	}
	log.Printf("CA certificate → %s", caOutPath)

	mux := http.NewServeMux()

	// POST /v1/sign
	// The OpAMP server sends the serialised ServerToAgent payload here.
	// This handler signs it and returns the raw signature bytes.
	//
	// Production note: decode the payload (proto.Unmarshal into
	// protobufs.ServerToAgent) here to apply policy before signing.
	mux.HandleFunc("POST /v1/sign", func(w http.ResponseWriter, r *http.Request) {
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		sig, err := signer.Sign(r.Context(), payload)
		if err != nil {
			http.Error(w, "sign: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(sig)
		log.Printf("[policy] signed %d-byte payload → %d-byte signature", len(payload), len(sig))
	})

	// GET /v1/chain
	// Returns the PEM-encoded signing certificate chain (leaf only here;
	// include any intermediates between the leaf and the root CA).
	// The root CA is excluded — agents already possess it as their trust anchor.
	mux.HandleFunc("GET /v1/chain", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-pem-file")
		_, _ = w.Write(leafPEM)
	})

	// GET /v1/ca
	// Returns the CA certificate in PEM.
	// Demo convenience only — not part of the OpAMP protocol. In
	// production, provision the CA cert out-of-band.
	mux.HandleFunc("GET /v1/ca", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-pem-file")
		_, _ = w.Write(caPEM)
	})

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("listen on %s: %v", listenAddr, err)
	}
	srv := &http.Server{Handler: mux}

	log.Printf("Policy server listening on %s", listenAddr)
	log.Println("Next steps (run from internal/examples/):")
	log.Printf("  OpAMP server: go run ./server --policy-server http://localhost%s", listenAddr)
	log.Printf("  Agent:        go run ./agent --attestation-ca %s", caOutPath)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	}()

	<-stop
	log.Println("Shutting down…")
	_ = srv.Shutdown(context.Background())
}
