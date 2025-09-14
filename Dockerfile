FROM golang:1.23 AS builder

WORKDIR /src
COPY . /src
RUN cd internal/examples/server && go build -o /bin/opamp-server .
RUN cd internal/examples/server && go build -o /bin/supervisor .

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# --------------------------------------------------

FROM debian:bookworm-slim
WORKDIR /app
COPY --from=builder /bin/opamp-server /bin/opamp-server
COPY --from=builder /bin/supervisor /bin/supervisor
COPY --from=builder /entrypoint.sh /entrypoint.sh
COPY internal/examples/server/uisrv/html ./uisrv/html
EXPOSE 4320 4321
ENTRYPOINT ["/entrypoint.sh"]
