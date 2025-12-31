# scale

Scale provides dumb agents to scale test an OpAMP server.

Websocket and HTTP servers are supported, but all agents must use the same connection type.
Each agent uses it's own OpAMP agent client, and runs in a goroutine.

The main driver logs to stdout, and all agents log to stderr.

## Usage

```
scale \
  -agent-count uint
    	The number of agents to start. (default 1000)
  -heartbeat duration
    	Heartbeat duration (default 30s)
  -server-url string
    	OpAMP server URL (default "wss://127.0.0.1:4320/v1/opamp")
  -tls-ca_file string
    	Path to the CA cert. It verifies the server certificate
  -tls-insecure
    	Disable the client transport security.
  -tls-insecure_skip_verify
    	Will enable TLS but not verify the certificate.
```
