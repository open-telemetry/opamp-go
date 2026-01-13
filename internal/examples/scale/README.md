# scale

Scale provides agents to scale test an OpAMP server.

Websocket and HTTP servers are supported, but all agents must use the same connection type.
Each agent uses it's own OpAMP agent client, and runs in a goroutine.

The main driver logs to stdout, and all agents log to stderr.

## Usage

Configuration may be specified through command line flags, or environment variables.

Configuration load priority is: `environment variables > flags > defaults`.

```
Usage of scale:
  -agent-count uint
    	The number of agents to start (env var: SCALE_AGENT_COUNT). (default 1000)
  -heartbeat duration
    	Heartbeat duration (env var: SCALE_HEARTBEAT). (default 30s)
  -server-url string
    	OpAMP server URL (env var: SCALE_SERVER_URL). (default "wss://127.0.0.1:4320/v1/opamp")
  -tls-ca_file string
    	Path to the OpAMP server CA cert (env var: SCALE_TLS_CA_FILE).
  -tls-insecure
    	Disable the client transport security (env var: SCALE_TLS_INSECURE).
  -tls-insecure_skip_verify
    	Will enable TLS but not verify the certificate (env var: SCALE_TLS_INSECURE_SKIP_VERIFY).
  -verbose-agents
    	Enable agent logging (env var: SCALE_VERBOSE_AGENTS).
```
