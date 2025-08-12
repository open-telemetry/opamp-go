# Testing Proxy

Start a proxy:

Using [tinyproxy](https://tinyproxy.github.io/) on the host.

Start tinyproxy with `systemctl start tinyproxy.service`

Start the server:
```
cd internal/examples/server
go run main.go
```

Start the agent:

Edit `internal/examples/agent/agent.go` to include

```go
	settings := types.StartSettings{
		OpAMPServerURL: "wss://127.0.0.1:4320/v1/opamp",
		ProxyURL:       "http://localhost:8888",
		ProxyHeaders:   http.Header{"test-key": []string{"test-val"}},
        ...
```
in the `Agent.connect` method (line 143).

Then start the agent:

```
cd internal/examples/agent
go run main.go
```
