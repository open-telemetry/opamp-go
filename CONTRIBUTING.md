# Contributing to opamp-go library

## Generate Protobuf Go Files

1. Make sure `opamp-go/internal/opamp-spec` submodule files exist. You can do this
either by cloning the `opamp-go` repo with submodules the first time, e.g. 
`git clone --recurse-submodules git@github.com:open-telemetry/opamp-go.git`.
Alternatively, if you already cloned `opamp-go` without the submodule then
execute `git submodule update --init` to fetch all files for `opamp-spec` submodule.
Note that `opamp-spec` submodule requires ssh git cloning access to github and won't
work if you only have https access.

2. Make sure you have Docker installed and running.

3. Run `make gen-proto`. This should compile `internal/proto/*.proto` files to 
`internal/protobufs/*.pb.go` files.

Note that this is tested on linux/amd64 and darwin/amd64 and is known to not work
on darwin/arm64 (M1/M2 Macs). Fixes are welcome.