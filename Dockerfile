# syntax=docker/dockerfile:1
FROM golang:1.23 AS builder

WORKDIR /src

# Copy all source code
COPY . /src

# Build OpAMP server
RUN cd internal/examples/server && go build -o /bin/opamp-server .

# Build Supervisor
RUN cd internal/examples/server && go build -o /bin/supervisor .

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# --------------------------------------------------

FROM debian:bookworm-slim

WORKDIR /app

# Copy binaries
COPY --from=builder /bin/opamp-server /bin/opamp-server
COPY --from=builder /bin/supervisor /bin/supervisor
COPY --from=builder /entrypoint.sh /entrypoint.sh

# Copy UI templates
COPY internal/examples/server/uisrv/html ./uisrv/html

# Expose ports
EXPOSE 4320 4321

# Start both server and supervisor
ENTRYPOINT ["/entrypoint.sh"]
