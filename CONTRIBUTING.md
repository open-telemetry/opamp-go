# Contributing to opamp-go library

## Introduction

Welcome to the opamp-go project! We are thrilled to have you here. Your contributions, whether big or small, play a crucial role in enhancing this project. Opamp-go is a Go implementation of the Open Agent Management Protocol (OpAMP), designed for the remote management of large fleets of data collection agents. This protocol allows agents to report their status, receive configurations, and obtain package updates from a server, fitting seamlessly within the broader OpenTelemetry ecosystem.

We deeply appreciate your interest in contributing and encourage you to ask questions and seek assistance within our community.


## Pre-requisites

Before you begin contributing, please ensure you have the following tools and software installed:

* **Go Programming Language**: Version 1.19 or higher.
* **Docker**: Ensure Docker is installed and running on your system.

**Platform Compatibility**:

- The project is tested on `linux/amd64` and `darwin/amd64`.
- It is known not to work on `darwin/arm64` (M1/M2 Macs). Contributions to address this are welcome.

## Workflow

When contributing to opamp-go, please follow these guidelines:

1. **Fork the Repository**: Create a personal fork of the opamp-go repository.
2. **Clone the Fork**: Clone your fork to your local machine.
3. **Create a Branch**: Use a descriptive name for your branch.
4. **Make Changes**: Implement your changes in the codebase.
5. **Commit Messages**: Write clear and concise commit messages.
6. **Push Changes**: Push your changes to your fork on GitHub.
7. **Open a Pull Request (PR)**: Navigate to the original repository and open a PR from your branch. Provide a detailed description of your changes.

## Local Run/Build

To set up and run the project locally:

1. **Clone with Submodules**:
   - If cloning for the first time with submodules:
     ```bash
     git clone --recurse-submodules git@github.com:open-telemetry/opamp-go.git
     ```
   - If you have already cloned without submodules:
     ```bash
     git submodule update --init
     ```
   - Note: The `opamp-spec` submodule requires SSH access to GitHub and won't work with HTTPS.

2. **Generate Protobuf Go Files**:
   - Ensure Docker is running.
   - Run:
     ```bash
     make gen-proto
     ```
   - This compiles `internal/proto/*.proto` files to `internal/protobufs/*.pb.go` files.

3. **Build the Project**:
   - Run:
     ```bash
     go build ./...
     ```

4. **Run Tests**:
   - Execute:
     ```bash
     go test ./...
     ```

# Releasing a new version of opamp-go

1. `Draft a new release` on the releases page in GitHub.

2. Create a new tag for the release and target the `main` branch.

3. Use the `Generate release notes` button to automatically generate release notes. Modify as appropriate.

4. Check `Set as a pre-release` as appropriate.

5. `Publish release`

## Further Help

If you need assistance:

* **Documentation**:
  - Refer to the [README](https://github.com/open-telemetry/opamp-go/blob/main/README.md) and other markdown files in the repository.

* **Contact Maintainers**:
  - Reach out via GitHub issues or pull requests for direct communication.

## Troubleshooting Guide

Encountering issues? Here are some common problems and solutions:

- **Submodule Errors**:
  - Ensure you have SSH access to GitHub.
  - Verify that submodules are initialized correctly.

- **Docker Issues**:
  - Ensure Docker is installed and running.
  - Restart the Docker daemon if needed.

If you experience further issues, feel free to reach out for support!
