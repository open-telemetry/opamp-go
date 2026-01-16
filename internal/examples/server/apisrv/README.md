# OpAMP Server REST API

A REST API for the OpAMP Server example that provides programmatic access to connected agents. This API decouples the UI layer from the server logic, enabling modern web applications, CLI tools, and other clients to interact with the OpAMP server.

## Quick Start

### Starting the Server

The API server starts automatically when you run the OpAMP server example:

```bash
cd internal/examples/server
go run .
```

The API server will be available at `http://localhost:4322`

### Testing the API

List all connected agents:

```bash
curl http://localhost:4322/api/v1/agents
```

Get a specific agent:

```bash
curl http://localhost:4322/api/v1/agents/{agent-uuid}
```

Update agent configuration:

```bash
curl -X POST http://localhost:4322/api/v1/agents/{agent-uuid}/config \
  -H "Content-Type: application/json" \
  -d '{"config": "receivers:\n  otlp:\n    protocols:\n      grpc:\n        endpoint: 0.0.0.0:4317\n"}'
```

## API Endpoints

### GET /api/v1/agents

Returns a list of all agents currently connected to the OpAMP server.

**Response:**

```json
{
  "data": [
    {
      "uuid": "019bc7b5-be9c-70b8-bad3-64c9807aa417",
      "status": {
        "instance_uid": "AZvHtb6ccLi602TJgHqkFw==",
        "sequence_num": "1",
        "agent_description": {
          "identifying_attributes": [
            {
              "key": "service.name",
              "value": {
                "Value": {
                  "StringValue": "io.opentelemetry.collector"
                }
              }
            }
          ]
        },
        "capabilities": "7167",
        "health": {
          "healthy": true
        }
      }
    }
  ]
}
```

### GET /api/v1/agents/{instanceid}

Retrieves detailed information about a specific agent by its instance ID.

**Parameters:**

- `instanceid` (path, required): UUID of the agent instance

**Responses:**

- `200 OK`: Agent found and returned
- `400 Bad Request`: Invalid UUID format
- `404 Not Found`: Agent not found

**Example:**

```bash
curl http://localhost:4322/api/v1/agents/019bc7b5-be9c-70b8-bad3-64c9807aa417
```

### POST /api/v1/agents/{instanceid}/config

Updates the configuration for a specific agent. The server sends the new configuration to the agent via OpAMP protocol and waits up to 5 seconds for acknowledgment.

**Parameters:**

- `instanceid` (path, required): UUID of the agent instance

**Request Body:**

```json
{
  "config": "receivers:\n  otlp:\n    protocols:\n      grpc:\n        endpoint: 0.0.0.0:4317\nexporters:\n  logging:\n    loglevel: debug\nservice:\n  pipelines:\n    traces:\n      receivers: [otlp]\n      exporters: [logging]\n"
}
```

**Responses:**

- `200 OK`: Configuration updated successfully
- `400 Bad Request`: Invalid UUID format or JSON
- `404 Not Found`: Agent not found
- `408 Request Timeout`: Agent didn't acknowledge within 5 seconds

**Example:**

```bash
curl -X POST http://localhost:4322/api/v1/agents/019bc7b5-be9c-70b8-bad3-64c9807aa417/config \
  -H "Content-Type: application/json" \
  -d @config.json
```

## Agent Status Fields

The `status` field in agent responses contains the full `AgentToServer` protobuf message with the following key fields:

### Core Fields

- **`instance_uid`**: Agent's persistent identifier (base64-encoded)
- **`sequence_num`**: Monotonically increasing counter for message ordering
- **`agent_description`**: Resource attributes identifying the agent
- **`capabilities`**: Bitmask of supported features (see below)
- **`health`**: Health status if the agent reports it
- **`effective_config`**: Current running configuration
- **`remote_config_status`**: Status of last remote config update
- **`package_statuses`**: Package management status

### Agent Description

#### Identifying Attributes

Uniquely identify the agent instance:

- `service.name` - Service name (required)
- `service.instance.id` - Instance identifier (recommended)
- `service.version` - Service version
- `service.namespace` - Service namespace (optional)

#### Non-Identifying Attributes

Describe the agent environment:

- `host.name` - Hostname
- `host.arch` - CPU architecture
- `os.type` - Operating system type
- `os.description` - OS version details

### Capabilities Bitmask

The `capabilities` field indicates what features the agent supports:

| Value  | Capability                            | Description                                  |
|--------|---------------------------------------|----------------------------------------------|
| 0x1    | ReportsStatus                         | Agent can report status                      |
| 0x2    | AcceptsRemoteConfig                   | Agent accepts remote configuration           |
| 0x4    | ReportsEffectiveConfig                | Agent reports its effective config           |
| 0x8    | AcceptsRestartCommand                 | Agent can be restarted remotely              |
| 0x10   | ReportsHealth                         | Agent reports health status                  |
| 0x20   | ReportsRemoteConfig                   | Agent reports remote config status           |
| 0x40   | AcceptsOpAMPConnectionSettings        | Agent accepts OpAMP connection settings      |
| 0x80   | AcceptsOtherConnectionSettings        | Agent accepts other connection settings      |
| 0x100  | AcceptsPackages                       | Agent accepts package installations          |
| 0x200  | ReportsPackageStatuses                | Agent reports package statuses               |
| 0x400  | ReportsOwnTraces                      | Agent reports its own traces                 |
| 0x800  | ReportsOwnMetrics                     | Agent reports its own metrics                |
| 0x1000 | ReportsOwnLogs                        | Agent reports its own logs                   |
| 0x2000 | AcceptsOpAMPConnectionSettingsRequest | Agent accepts connection settings requests   |
| 0x4000 | ReportsAvailableComponents            | Agent reports available components           |

### Effective Configuration

Configuration is stored base64-encoded in the `body` field:

```json
{
  "effective_config": {
    "config_map": {
      "config_map": {
        "": {
          "body": "cmVjZWl2ZXJzOgogIG90bHA6CiAgICBwcm90b2NvbHM6...",
          "content_type": "text/yaml"
        }
      }
    }
  }
}
```

**Note**: Decode the `body` field from base64 to get the actual configuration content.

## CORS Support

The API includes CORS middleware that allows cross-origin requests from any origin. This enables web applications hosted on different domains to interact with the API.

CORS headers:

- `Access-Control-Allow-Origin: *`
- `Access-Control-Allow-Methods: GET, POST, PUT, DELETE, OPTIONS`
- `Access-Control-Allow-Headers: Content-Type, Authorization`

## Error Handling

All error responses follow a consistent format:

```json
{
  "error": "Error message describing what went wrong"
}
```

Common HTTP status codes:

- `200 OK`: Request successful
- `400 Bad Request`: Invalid input (malformed UUID, invalid JSON)
- `404 Not Found`: Resource not found
- `408 Request Timeout`: Operation timed out
- `500 Internal Server Error`: Server error

## Architecture

The API server is designed to be a thin layer over the existing OpAMP server implementation:

```text
┌─────────────┐
│   Clients   │ (Web UI, CLI, etc.)
└──────┬──────┘
       │ HTTP/REST
       ▼
┌─────────────┐
│  API Server │ (Port 4322)
└──────┬──────┘
       │
       ▼
┌─────────────┐
│   Agents    │ (In-memory store)
│   Manager   │
└──────┬──────┘
       │
       ▼
┌─────────────┐
│ OpAMP Server│ (Port 4320)
└──────┬──────┘
       │ WebSocket
       ▼
┌─────────────┐
│   Agents    │
└─────────────┘
```

### Components

- **ApiServer**: HTTP server handling REST requests
- **Agents Manager**: Shared in-memory store of connected agents
- **OpAMP Server**: WebSocket server implementing OpAMP protocol

## Testing

Run the test suite:

```bash
cd internal/examples
go test -v ./server/apisrv/
```

The tests cover:

- Server lifecycle (start/stop)
- Listing multiple agents
- Retrieving individual agents
- Updating agent configurations
- Error handling (invalid UUIDs, non-existent agents)

## OpenAPI Specification

A complete OpenAPI 3.0 specification is available in `openapi.yaml`. You can use it with tools like:

- **Swagger UI**: Interactive API documentation
- **Postman**: Import and test the API
- **OpenAPI Generator**: Generate client SDKs

View the spec with Swagger UI:

```bash
# Using Docker
docker run -p 8080:8080 -e SWAGGER_JSON=/api/openapi.yaml \
  -v $(pwd):/api swaggerapi/swagger-ui

# Then open http://localhost:8080
```

## Development

### Adding New Endpoints

1. Add handler method to `ApiServer` struct in `api.go`
2. Register route in `Start()` method
3. Add tests in `api_test.go`
4. Update OpenAPI spec in `openapi.yaml`
5. Update this README

### Code Structure

```text
apisrv/
├── api.go          # API server implementation
├── api_test.go     # Comprehensive test suite
├── openapi.yaml    # OpenAPI 3.0 specification
└── README.md       # This file
```

## References

- [OpAMP Specification](https://github.com/open-telemetry/opamp-spec)
- [OpAMP Protocol Buffers](https://github.com/open-telemetry/opamp-spec/blob/main/proto/opamp.proto)
- [GitHub Issue #474](https://github.com/open-telemetry/opamp-go/issues/474)

## Future Enhancements

Potential additions to consider:

- Pagination for agent lists
- Filtering and searching agents
- WebSocket endpoint for real-time updates
- Agent restart command endpoint
- Package management endpoints
- Metrics and health endpoints
- Authentication and authorization
