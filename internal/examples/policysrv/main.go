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
//
// # Demonstrating signing certificate rotation
//
// The signing leaf can rotate under the same (unchanged) root CA while
// agents stay connected. Rotate on an interval:
//
//	go run ./policysrv --rotate-interval 30s
//
// or trigger a rotation on demand:
//
//	curl -X POST http://localhost:4322/v1/rotate
//
// Because the root CA — the agent's trust anchor — never changes, connected
// agents re-validate the new chain and keep verifying without reconnecting.
package main

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/open-telemetry/opamp-go/signing"
)

// signerState holds the current signing leaf under a fixed root CA. rotate
// swaps in a fresh leaf (same root), mimicking rc-x509's frequent signing-key
// rotation: the payload trust anchor never changes, but the chain to it does.
// This exercises the Agent's mid-connection re-validation on rotation.
type signerState struct {
	ca       *x509.Certificate
	caKey    crypto.Signer
	leafOpts signing.CertOptions

	mu      sync.Mutex
	signer  signing.Signer
	leafPEM []byte
}

func (s *signerState) rotate() error {
	leaf, leafKey, err := signing.GenerateLeaf(signing.AlgorithmECDSAP256SHA256, s.ca, s.caKey, s.leafOpts)
	if err != nil {
		return err
	}
	ls, err := signing.NewLocalSigner(leafKey, []*x509.Certificate{leaf})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.signer = ls.WithRootCA(s.ca)
	s.leafPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})
	s.mu.Unlock()
	return nil
}

func (s *signerState) sign(ctx context.Context, payload []byte) ([]byte, error) {
	s.mu.Lock()
	signer := s.signer
	s.mu.Unlock()
	return signer.Sign(ctx, payload)
}

func (s *signerState) chainPEM() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leafPEM
}

const (
	listenAddr = ":4322"
	caOutPath  = "/tmp/opamp-policy-ca.pem"
)

func main() {
	var rotateInterval time.Duration
	flag.DurationVar(&rotateInterval, "rotate-interval", 0,
		"if >0, rotate the signing leaf on this interval (e.g. 30s) to demonstrate mid-connection rotation")
	flag.Parse()

	// Generate an ephemeral root CA. In production: load the CA and leaf
	// private key from an HSM or secrets manager; never write the private
	// key to disk.
	ca, caKey, err := signing.GenerateCA(signing.AlgorithmECDSAP256SHA256, signing.CertOptions{})
	if err != nil {
		log.Fatalf("generate CA: %v", err)
	}
	state := &signerState{
		ca:    ca,
		caKey: caKey,
		leafOpts: signing.CertOptions{
			// SAN required by the spec: the leaf must match the OpAMP distribution server's hostname.
			// The example server binds to 0.0.0.0:4320; agents may connect by hostname or IP, so
			// include both. Production deployments set these to the actual hostname(s) or IP(s).
			DNSNames:    []string{"localhost"},
			IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		},
	}
	if err := state.rotate(); err != nil {
		log.Fatalf("generate signing leaf: %v", err)
	}

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})

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
		sig, err := state.sign(r.Context(), payload)
		if err != nil {
			http.Error(w, "sign: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(sig)
		log.Printf("[policy] signed %d-byte payload → %d-byte signature", len(payload), len(sig))
	})

	// GET /v1/chain
	// Returns the PEM-encoded current signing certificate chain (leaf only
	// here; include any intermediates between the leaf and the root CA).
	// The root CA is excluded — agents already possess it as their trust anchor.
	mux.HandleFunc("GET /v1/chain", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-pem-file")
		_, _ = w.Write(state.chainPEM())
	})

	// POST /v1/rotate
	// Rotates the signing leaf on demand under the same root CA. The root —
	// the agent's trust anchor — is unchanged; only the chain to it changes,
	// so connected agents re-validate and re-pin without dropping the
	// connection. Demo/testing convenience only; not part of the OpAMP protocol.
	mux.HandleFunc("POST /v1/rotate", func(w http.ResponseWriter, r *http.Request) {
		if err := state.rotate(); err != nil {
			http.Error(w, "rotate: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("[policy] rotated signing leaf")
		w.WriteHeader(http.StatusNoContent)
	})

	// GET /v1/ca
	// Returns the CA certificate in PEM.
	// Demo convenience only — not part of the OpAMP protocol. In
	// production, provision the CA cert out-of-band.
	mux.HandleFunc("GET /v1/ca", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-pem-file")
		_, _ = w.Write(caPEM)
	})

	if rotateInterval > 0 {
		go func() {
			ticker := time.NewTicker(rotateInterval)
			defer ticker.Stop()
			for range ticker.C {
				if err := state.rotate(); err != nil {
					log.Printf("[policy] rotate failed: %v", err)
					continue
				}
				log.Printf("[policy] rotated signing leaf (interval %s)", rotateInterval)
			}
		}()
		log.Printf("Auto-rotating signing leaf every %s", rotateInterval)
	}

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
