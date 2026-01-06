package agent

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/knadh/koanf"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/rawbytes"

	"github.com/open-telemetry/opamp-go/client"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/internal/examples/config"
	"github.com/open-telemetry/opamp-go/protobufs"
)

var localConfig = []byte(`
exporters:
  otlp:
    endpoint: localhost:1111

receivers:
  otlp:
    protocols:
      grpc: {}
      http: {}

service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: []
      exporters: [otlp]
`)

// Agent identification constants
const (
	agentType    = "io.opentelemetry.collector"
	agentVersion = "1.0.0"
)

type Agent struct {
	client client.OpAMPClient
	logger types.Logger
	doneCh chan struct{}

	agentType    string
	agentVersion string
	instanceId   uuid.UUID

	agentConfig *config.AgentConfig

	effectiveConfig []byte

	agentDescription *protobufs.AgentDescription

	remoteConfigStatus *protobufs.RemoteConfigStatus

	metricReporter *MetricReporter

	// The TLS certificate used for the OpAMP connection. Can be nil, meaning no client-side
	// certificate is used.
	opampClientCert *tls.Certificate

	tlsConfig     *tls.Config
	proxySettings *proxySettings

	certRequested       bool
	clientPrivateKeyPEM []byte
}

type proxySettings struct {
	url     string
	headers http.Header
}

func (p *proxySettings) Clone() *proxySettings {
	return &proxySettings{
		url:     p.url,
		headers: p.headers.Clone(),
	}
}

type Option func(agent *Agent)

// WithLogger is used to set an Agent's logger
func WithLogger(l types.Logger) Option {
	return func(agent *Agent) {
		agent.logger = l
	}
}

// WithLogger is used to set an Agent's type
func WithAgentType(s string) Option {
	return func(agent *Agent) {
		agent.agentType = s
	}
}

// WithLogger is used to set an Agent's version
func WithAgentVersion(s string) Option {
	return func(agent *Agent) {
		agent.agentVersion = s
	}
}

// WithLogger is used to set an Agent's id
func WithInstanceID(id uuid.UUID) Option {
	return func(agent *Agent) {
		agent.instanceId = id
	}
}

// WithNoClientCertRequest will ensure the agent does not request a client cert when initially connecting.
func WithNoClientCertRequest() Option {
	return func(agent *Agent) {
		agent.certRequested = true
	}
}

func NewAgent(agentConfig *config.AgentConfig, options ...Option) *Agent {
	agent := &Agent{
		logger:          &Logger{Logger: log.Default()},
		agentType:       agentType,
		agentVersion:    agentVersion,
		agentConfig:     agentConfig,
		effectiveConfig: localConfig,
	}

	for _, option := range options {
		option(agent)
	}

	agent.createAgentIdentity()
	agent.loadLocalConfig()

	return agent
}

type settingsOp func(*types.StartSettings)

// withTLSConfig sets the StartSettings.TLSConfig option.
func withTLSConfig(tlsConfig *tls.Config) settingsOp {
	return func(settings *types.StartSettings) {
		settings.TLSConfig = tlsConfig
	}
}

// withProxy sets the StartSettings.ProxyURL and StartSettings.ProxyHeaders options.
func withProxy(proxy *proxySettings) settingsOp {
	return func(settings *types.StartSettings) {
		if proxy == nil {
			return
		}
		settings.ProxyURL = proxy.url
		settings.ProxyHeaders = proxy.headers
	}
}

func (agent *Agent) connect(ops ...settingsOp) error {
	if strings.HasPrefix(agent.agentConfig.Endpoint, "http") {
		agent.client = client.NewHTTP(agent.logger)
	} else if strings.HasPrefix(agent.agentConfig.Endpoint, "ws") {
		agent.client = client.NewWebSocket(agent.logger)
	} else {
		return fmt.Errorf("server endpoint has unknown scheme: %s", agent.agentConfig.Endpoint)
	}

	settings := types.StartSettings{
		OpAMPServerURL:    agent.agentConfig.Endpoint,
		HeartbeatInterval: agent.agentConfig.HeartbeatInterval,
		InstanceUid:       types.InstanceUid(agent.instanceId),
		Callbacks: types.Callbacks{
			OnConnect: func(ctx context.Context) {
				agent.logger.Debugf(ctx, "Connected to the server.")
			},
			OnConnectFailed: func(ctx context.Context, err error) {
				agent.logger.Errorf(ctx, "Failed to connect to the server: %v", err)
			},
			OnError: func(ctx context.Context, err *protobufs.ServerErrorResponse) {
				agent.logger.Errorf(ctx, "Server returned an error response: %v", err.ErrorMessage)
			},
			SaveRemoteConfigStatus: func(_ context.Context, status *protobufs.RemoteConfigStatus) {
				agent.remoteConfigStatus = status
			},
			GetEffectiveConfig: func(ctx context.Context) (*protobufs.EffectiveConfig, error) {
				return agent.composeEffectiveConfig(), nil
			},
			OnMessage:                 agent.onMessage,
			OnOpampConnectionSettings: agent.onOpampConnectionSettings,
			OnConnectionSettings:      agent.onConnectionSettings,
		},
		RemoteConfigStatus: agent.remoteConfigStatus,
	}
	for _, op := range ops {
		op(&settings)
	}
	agent.tlsConfig = settings.TLSConfig
	agent.proxySettings = &proxySettings{
		url:     settings.ProxyURL,
		headers: settings.ProxyHeaders,
	}

	err := agent.client.SetAgentDescription(agent.agentDescription)
	if err != nil {
		return err
	}

	supportedCapabilities := protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsRemoteConfig |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsEffectiveConfig |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsOwnMetrics |
		protobufs.AgentCapabilities_AgentCapabilities_AcceptsOpAMPConnectionSettings |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsConnectionSettingsStatus
	err = agent.client.SetCapabilities(&supportedCapabilities)
	if err != nil {
		return err
	}

	// This sets the request to create a client certificate before the OpAMP client
	// is started, before the connection is established. However, this assumes the
	// server supports "AcceptsConnectionRequest" capability.
	// Alternatively the agent can perform this request after receiving the first
	// message from the server (in onMessage), i.e. after the server capabilities
	// become known and can be checked.
	agent.requestClientCertificate()

	agent.logger.Debugf(context.Background(), "Starting OpAMP client...")

	err = agent.client.Start(context.Background(), settings)
	if err != nil {
		return err
	}

	agent.logger.Debugf(context.Background(), "OpAMP Client started.")

	return nil
}

func (agent *Agent) disconnect(ctx context.Context) {
	agent.logger.Debugf(ctx, "Disconnecting from server...")
	agent.client.Stop(ctx)
}

// createAgentIdentity sets the instanceId if it is not already set and populates agentDescription.
func (agent *Agent) createAgentIdentity() {
	// Generate instance id.
	if agent.instanceId == uuid.Nil {
		uid, err := uuid.NewV7()
		if err != nil {
			panic(err)
		}
		agent.instanceId = uid
	}

	hostname, _ := os.Hostname()

	// Create Agent description.
	agent.agentDescription = &protobufs.AgentDescription{
		IdentifyingAttributes: []*protobufs.KeyValue{
			{
				Key: "service.name",
				Value: &protobufs.AnyValue{
					Value: &protobufs.AnyValue_StringValue{StringValue: agent.agentType},
				},
			},
			{
				Key: "service.version",
				Value: &protobufs.AnyValue{
					Value: &protobufs.AnyValue_StringValue{StringValue: agent.agentVersion},
				},
			},
		},
		NonIdentifyingAttributes: []*protobufs.KeyValue{
			{
				Key: "os.type",
				Value: &protobufs.AnyValue{
					Value: &protobufs.AnyValue_StringValue{
						StringValue: runtime.GOOS,
					},
				},
			},
			{
				Key: "host.name",
				Value: &protobufs.AnyValue{
					Value: &protobufs.AnyValue_StringValue{
						StringValue: hostname,
					},
				},
			},
		},
	}
}

func (agent *Agent) updateAgentIdentity(ctx context.Context, instanceId uuid.UUID) {
	agent.logger.Debugf(ctx, "Agent identify is being changed from id=%v to id=%v",
		agent.instanceId,
		instanceId)
	agent.instanceId = instanceId

	if agent.metricReporter != nil {
		// TODO: reinit or update meter (possibly using a single function to update all own connection settings
		// or with having a common resource factory or so)
	}
}

// loadLocalConfig sets effectiveConfig
func (agent *Agent) loadLocalConfig() {
	k := koanf.New(".")
	_ = k.Load(rawbytes.Provider(localConfig), yaml.Parser())

	effectiveConfigBytes, err := k.Marshal(yaml.Parser())
	if err != nil {
		panic(err)
	}

	agent.effectiveConfig = effectiveConfigBytes
}

func (agent *Agent) composeEffectiveConfig() *protobufs.EffectiveConfig {
	return &protobufs.EffectiveConfig{
		ConfigMap: &protobufs.AgentConfigMap{
			ConfigMap: map[string]*protobufs.AgentConfigFile{
				"": {Body: agent.effectiveConfig},
			},
		},
	}
}

func (agent *Agent) initMeter(settings *protobufs.TelemetryConnectionSettings) error {
	reporter, err := NewMetricReporter(agent.logger, settings, agent.agentType, agent.agentVersion, agent.instanceId)
	if err != nil {
		agent.logger.Errorf(context.Background(), "Cannot collect metrics: %v", err)
		return err
	}

	prevReporter := agent.metricReporter

	agent.metricReporter = reporter

	if prevReporter != nil {
		prevReporter.Shutdown()
	}

	return nil
}

type agentConfigFileItem struct {
	name string
	file *protobufs.AgentConfigFile
}

type agentConfigFileSlice []agentConfigFileItem

func (a agentConfigFileSlice) Less(i, j int) bool {
	return a[i].name < a[j].name
}

func (a agentConfigFileSlice) Swap(i, j int) {
	t := a[i]
	a[i] = a[j]
	a[j] = t
}

func (a agentConfigFileSlice) Len() int {
	return len(a)
}

func (agent *Agent) applyRemoteConfig(config *protobufs.AgentRemoteConfig) (configChanged bool, err error) {
	if config == nil {
		return false, nil
	}

	agent.logger.Debugf(context.Background(), "Received remote config from server, hash=%x.", config.ConfigHash)

	// Begin with local config. We will later merge received configs on top of it.
	k := koanf.New(".")
	if err := k.Load(rawbytes.Provider(localConfig), yaml.Parser()); err != nil {
		return false, err
	}

	orderedConfigs := agentConfigFileSlice{}
	for name, file := range config.Config.ConfigMap {
		if name == "" {
			// skip instance config
			continue
		}
		orderedConfigs = append(orderedConfigs, agentConfigFileItem{
			name: name,
			file: file,
		})
	}

	// Sort to make sure the order of merging is stable.
	sort.Sort(orderedConfigs)

	// Append instance config as the last item.
	instanceConfig := config.Config.ConfigMap[""]
	if instanceConfig != nil {
		orderedConfigs = append(orderedConfigs, agentConfigFileItem{
			name: "",
			file: instanceConfig,
		})
	}

	// Merge received configs.
	for _, item := range orderedConfigs {
		k2 := koanf.New(".")
		err := k2.Load(rawbytes.Provider(item.file.Body), yaml.Parser())
		if err != nil {
			return false, fmt.Errorf("cannot parse config named %s: %v", item.name, err)
		}
		err = k.Merge(k2)
		if err != nil {
			return false, fmt.Errorf("cannot merge config named %s: %v", item.name, err)
		}
	}

	// The merged final result is our effective config.
	effectiveConfigBytes, err := k.Marshal(yaml.Parser())
	if err != nil {
		panic(err)
	}

	configChanged = false
	if !bytes.Equal(agent.effectiveConfig, effectiveConfigBytes) {
		agent.logger.Debugf(context.Background(), "Effective config changed. Need to report to server.")
		agent.effectiveConfig = effectiveConfigBytes
		configChanged = true
	}

	return configChanged, nil
}

func (agent *Agent) Start() error {
	tls, err := agent.agentConfig.GetTLSConfig(context.Background())
	if err != nil {
		return err
	}
	agent.logger.Debugf(context.Background(), "Agent starting, id=%v, type=%s, version=%s.",
		agent.instanceId, agent.agentType, agent.agentVersion)
	agent.doneCh = make(chan struct{})
	return agent.connect(withTLSConfig(tls))
}

func (agent *Agent) Shutdown() {
	if agent.doneCh == nil {
		agent.logger.Debugf(context.Background(), "Agent not running.")
		return
	}
	agent.logger.Debugf(context.Background(), "Agent shutting down...")
	if agent.client != nil {
		_ = agent.client.Stop(context.Background())
	}
	close(agent.doneCh)
}

func (agent *Agent) Wait() {
	<-agent.doneCh
}

// requestClientCertificate sets a request to be sent to the Server to create
// a client certificate that the Agent can use in subsequent OpAMP connections.
// This is the initiating step of the Client Signing Request (CSR) flow.
func (agent *Agent) requestClientCertificate() {
	if agent.certRequested {
		// Request only once, for bootstrapping.
		// TODO: the Agent may also for example check that the current certificate
		// is approaching expiration date and re-requests a new certificate.
		return
	}

	// Generate a keypair for new client cert.
	clientCertKeyPair, err := rsa.GenerateKey(cryptorand.Reader, 4096)
	if err != nil {
		agent.logger.Errorf(context.Background(), "Cannot generate keypair: %v", err)
		return
	}

	// Encode the private key of the keypair as DER.
	privateKeyDER := x509.MarshalPKCS1PrivateKey(clientCertKeyPair)

	// Convert private key from DER to PEM.
	privateKeyPEM := new(bytes.Buffer)
	pem.Encode(
		privateKeyPEM, &pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: privateKeyDER,
		},
	)
	// Keep it. We will need it in later steps of the flow.
	agent.clientPrivateKeyPEM = privateKeyPEM.Bytes()

	// Create the CSR.
	template := x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName:   "OpAMP Example Client",
			Organization: []string{"OpenTelemetry OpAMP Workgroup"},
			Locality:     []string{"Agent-initiated"},
			// Where do we put instance_uid?
		},
		SignatureAlgorithm: x509.SHA256WithRSA,
	}

	derBytes, err := x509.CreateCertificateRequest(cryptorand.Reader, &template, clientCertKeyPair)
	if err != nil {
		agent.logger.Errorf(context.Background(), "Failed to create certificate request: %s", err)
		return
	}

	// Convert CSR from DER to PEM format.
	csrPEM := new(bytes.Buffer)
	pem.Encode(
		csrPEM, &pem.Block{
			Type:  "CERTIFICATE REQUEST",
			Bytes: derBytes,
		},
	)

	// Send the request to the Server (immediately if already connected
	// or upon next successful connection).
	err = agent.client.RequestConnectionSettings(
		&protobufs.ConnectionSettingsRequest{
			Opamp: &protobufs.OpAMPConnectionSettingsRequest{
				CertificateRequest: &protobufs.CertificateRequest{
					Csr: csrPEM.Bytes(),
				},
			},
		},
	)
	if err != nil {
		agent.logger.Errorf(context.Background(), "Failed to send CSR to server: %s", err)
		return
	}

	agent.certRequested = true
}

func (agent *Agent) onMessage(ctx context.Context, msg *types.MessageData) {
	configChanged := false
	if msg.RemoteConfig != nil {
		var err error
		configChanged, err = agent.applyRemoteConfig(msg.RemoteConfig)
		if err != nil {
			agent.client.SetRemoteConfigStatus(
				&protobufs.RemoteConfigStatus{
					LastRemoteConfigHash: msg.RemoteConfig.ConfigHash,
					Status:               protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED,
					ErrorMessage:         err.Error(),
				},
			)
		} else {
			agent.client.SetRemoteConfigStatus(&protobufs.RemoteConfigStatus{
				LastRemoteConfigHash: msg.RemoteConfig.ConfigHash,
				Status:               protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED,
			})
		}
	}

	if msg.AgentIdentification != nil {
		uid, err := uuid.FromBytes(msg.AgentIdentification.NewInstanceUid)
		if err != nil {
			agent.logger.Errorf(ctx, "invalid NewInstanceUid: %v", err)
			return
		}
		agent.updateAgentIdentity(ctx, uid)
	}

	if configChanged {
		err := agent.client.UpdateEffectiveConfig(ctx)
		if err != nil {
			agent.logger.Errorf(ctx, err.Error())
		}
	}

	// TODO: check that the Server has AcceptsConnectionSettingsRequest capability before
	// requesting a certificate.
	// This is actually a no-op since we already made the request when connecting
	// (see connect()). However we keep this call here to demonstrate that requesting it
	// in onMessage callback is also an option. This approach should be used if it is
	// necessary to check for AcceptsConnectionSettingsRequest (if the Agent is
	// not certain that the Server has this capability).
	agent.requestClientCertificate()
}

func (agent *Agent) tryChangeOpAMP(ctx context.Context, cert *tls.Certificate, tlsConfig *tls.Config, proxy *proxySettings) {
	agent.logger.Debugf(ctx, "Reconnecting to verify new OpAMP settings.\n")
	agent.disconnect(ctx)

	oldCfg := agent.tlsConfig
	if tlsConfig == nil {
		tlsConfig = oldCfg.Clone()
	}
	if cert != nil {
		agent.logger.Debugf(ctx, "Using new certificate\n")
		tlsConfig.Certificates = []tls.Certificate{*cert}
	}

	if proxy != nil {
		agent.logger.Debugf(ctx, "Proxy settings revieved: %v\n", proxy)
	}

	oldProxy := agent.proxySettings
	if proxy == nil && oldProxy != nil {
		proxy = oldProxy.Clone()
	}

	if err := agent.connect(withTLSConfig(tlsConfig), withProxy(proxy)); err != nil {
		agent.logger.Errorf(ctx, "Cannot connect after using new tls config: %s. Ignoring the offer\n", err)
		if err := agent.connect(withTLSConfig(oldCfg), withProxy(oldProxy)); err != nil {
			agent.logger.Errorf(ctx, "Unable to reconnect after restoring tls config: %s\n", err)
		}
		return
	}

	agent.logger.Debugf(ctx, "Successfully connected to server. Accepting new tls config.\n")
	// TODO: we can also persist the successfully accepted settigns and use it when the
	// agent connects to the server after the restart.
}

func (agent *Agent) onOpampConnectionSettings(ctx context.Context, settings *protobufs.OpAMPConnectionSettings) error {
	if settings == nil {
		agent.logger.Debugf(ctx, "Received nil settings, ignoring.\n")
		return nil
	}

	var cert *tls.Certificate
	var err error
	if settings.Certificate != nil {
		cert, err = agent.getCertFromSettings(settings.Certificate)
		if err != nil {
			return err
		}
	}

	var tlsConfig *tls.Config
	if settings.Tls != nil {
		tlsMin, err := getTLSVersionNumber(settings.Tls.MinVersion)
		if err != nil {
			return fmt.Errorf("unable to convert settings.tls.min_version: %w", err)
		}
		tlsMax, err := getTLSVersionNumber(settings.Tls.MaxVersion)
		if err != nil {
			return fmt.Errorf("unable to convert settings.tls.max_version: %w", err)
		}

		tlsConfig = &tls.Config{
			InsecureSkipVerify: settings.Tls.InsecureSkipVerify,
			MinVersion:         tlsMin,
			MaxVersion:         tlsMax,
			RootCAs:            x509.NewCertPool(),
			// TODO support cipher_suites values
		}

		if settings.Tls.IncludeSystemCaCertsPool {
			tlsConfig.RootCAs, err = x509.SystemCertPool()
			if err != nil {
				return fmt.Errorf("unable to use system cert pool: %w", err)
			}
		}

		if settings.Tls.CaPemContents != "" {
			ok := tlsConfig.RootCAs.AppendCertsFromPEM([]byte(settings.Tls.CaPemContents))
			if !ok {
				return fmt.Errorf("unable to add PEM CA")
			}
			agent.logger.Debugf(ctx, "CA in offered settings.\n")
		}
	}

	// proxy settings
	var proxy *proxySettings
	if settings.Proxy != nil {
		proxy = &proxySettings{
			url:     settings.Proxy.Url,
			headers: toHeaders(settings.Proxy.ConnectHeaders),
		}
	}
	// TODO: also use settings.DestinationEndpoint and settings.Headers for future connections.
	go agent.tryChangeOpAMP(ctx, cert, tlsConfig, proxy)

	return nil
}

func getTLSVersionNumber(input string) (uint16, error) {
	switch strings.ToUpper(input) {
	case "1.0", "TLSV1", "TLSV1.0":
		return tls.VersionTLS10, nil
	case "1.1", "TLSV1.1":
		return tls.VersionTLS11, nil
	case "1.2", "TLSV1.2":
		return tls.VersionTLS12, nil
	case "1.3", "TLSV1.3":
		return tls.VersionTLS13, nil
	case "":
		// Do nothing if no value is set
		return 0, nil
	default:
		return 0, fmt.Errorf("unsupported value: %s", input)
	}
}

func (agent *Agent) getCertFromSettings(certificate *protobufs.TLSCertificate) (*tls.Certificate, error) {
	// Parse the key pair to a TLS certificate that can be used for network connections.

	// There are 2 types of certificate creation flows in OpAMP: client-initiated CSR
	// and server-initiated. In this example we demonstrate both flows.
	// Real-world Agent implementations will probably choose and use only one of these flows.

	var cert tls.Certificate
	var err error
	if certificate.PrivateKey == nil && agent.clientPrivateKeyPEM != nil {
		// Client-initiated CSR flow. This is currently initiated when connecting
		// to the Server for the first time (see requestClientCertificate()).
		cert, err = tls.X509KeyPair(
			certificate.Cert,          // We received the certificate from the Server.
			agent.clientPrivateKeyPEM, // Private key was earlier locally generated.
		)
	} else {
		// Server-initiated flow. This is currently initiated by user clicking a button in
		// the Server UI.
		// Both certificate and private key are from the Server.
		cert, err = tls.X509KeyPair(
			certificate.Cert,
			certificate.PrivateKey,
		)
	}

	if err != nil {
		agent.logger.Errorf(context.Background(), "Received invalid certificate offer: %s\n", err.Error())
		return nil, err
	}

	if len(certificate.CaCert) != 0 {
		caCertPB, _ := pem.Decode(certificate.CaCert)
		caCert, err := x509.ParseCertificate(caCertPB.Bytes)
		if err != nil {
			agent.logger.Errorf(context.Background(), "Cannot parse CA cert: %v", err)
			return nil, err
		}
		agent.logger.Debugf(context.Background(), "Received offer signed by CA: %v", caCert.Subject)
		// TODO: we can verify the CA's identity here (to match our CA as we know it).
	}

	return &cert, nil
}

// toHeaders transforms a *protobufs.Headers to an http.Header
func toHeaders(ph *protobufs.Headers) http.Header {
	var header http.Header
	if ph == nil {
		return header
	}
	for _, h := range ph.Headers {
		header.Set(h.Key, h.Value)
	}
	return header
}

func (agent *Agent) onConnectionSettings(ctx context.Context, settings *protobufs.ConnectionSettingsOffers) error {
	agent.logger.Debugf(context.Background(), "Received connection settings offers from server, hash=%x.", settings.Hash)
	// TODO handle traces, logs, and other connection settings
	if settings.OwnMetrics != nil {
		return agent.initMeter(settings.OwnMetrics)
	}
	return nil
}
