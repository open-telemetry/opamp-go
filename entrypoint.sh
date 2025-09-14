#!/bin/sh


sleep 2

/bin/supervisor --server-url ws://127.0.0.1:4320/v1/opamp &

wait
