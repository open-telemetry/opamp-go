# agent

Agent provides provides and example agent implementation for the OpAMP protocol.

Both HTTP and Websocket connections are supported.

The example agent can be in a normal mode; where the binary starts a single agent, or in scale mode (when `-run-scale` is passed or `AGENT_RUN_SCALE=true` is set).

When in scale mode the process will start multiple agents (up to `-agent-scale-count/AGENT_SCALE_COUNT`) in the same process.
All agents will use the same scheme when connection to the OpAMP server (HTTP/Websocket).

In scale mode, the agent orchestartor will log to stdout, and the agents to stderr.

## Usage

```
Usage of agent:
  -endpoint string
    	OpAMP server endpoint URL (env var: AGENT_ENDPOINT). (default "wss://127.0.0.1:4320/v1/opamp")
  -heartbeat duration
    	Heartbeat duration (env var: AGENT_HEARTBEAT). (default 30s)
  -quite-agent
    	Disable agent logger (env var: AGENT_QUITE).
  -run-scale
    	Run in scale-test mode (env var: AGENT_RUN_SCALE).
  -scale-count uint
    	The number of agents to start in scale mode (env var: AGENT_SCALE_COUNT). (default 1000)
  -t string
    	Agent Type String (env var: AGENT_TYPE). (default "io.opentelemetry.collector")
  -tls-ca_file string
    	Path to the CA cert. It verifies the server certificate (env var: AGENT_TLS_CA_FILE).
  -tls-cert_file string
    	Path to the TLS cert (env var: AGENT_TLS_CERT_FILE).
  -tls-insecure
    	Disable the client transport security (env var: AGENT_TLS_INSECURE).
  -tls-insecure_skip_verify
    	Will enable TLS but not verify the certificate (env var: AGENT_TLS_INSECURE_SKIP_VERIFY).
  -tls-key_file string
    	Path to the TLS key (env var: AGENT_TLS_KEY_FILE).
  -v string
    	Agent Version String (env var: AGENT_VERSION). (default "1.0.0")
```
