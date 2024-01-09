# OpAMP protocol implementation in Go

---

<p align="center">
  <strong>
    <a href="https://cloud-native.slack.com/archives/C02J58HR58R">Getting In Touch</a>
  </strong>
</p>

<p align="center">
  <a href="https://pkg.go.dev/github.com/open-telemetry/opamp-go">
    <img alt="Go Docs" height="28" src="https://godoc.org/github.com/open-telemetry/opamp-go?status.svg"></a>
  <a href="https://github.com/open-telemetry/opamp-go/actions/workflows/build-and-test.yml?query=branch%3Amain">
    <img alt="Build Status" src="https://img.shields.io/github/actions/workflow/status/open-telemetry/opamp-go/build-and-test.yml?branch%3Amain&style=for-the-badge"></a>
  <a href="https://goreportcard.com/report/github.com/open-telemetry/opamp-go">
    <img alt="Go Report Card" src="https://goreportcard.com/badge/github.com/open-telemetry/opamp-go?style=for-the-badge"></a>
  <a href="https://codecov.io/gh/open-telemetry/opamp-go/branch/main/">
    <img alt="Codecov Status" src="https://img.shields.io/codecov/c/github/open-telemetry/opamp-go?style=for-the-badge"></a>
  <a href="https://github.com/open-telemetry/opamp-go/releases">
    <img alt="GitHub release (latest by date including pre-releases)" src="https://img.shields.io/github/v/release/open-telemetry/opamp-go?include_prereleases&style=for-the-badge"></a>
</p>

---

[Open Agent Management Protocol (OpAMP)](https://github.com/open-telemetry/opamp-spec)
is a network protocol for remote management of large fleets of data collection Agents.

OpAMP allows Agents to report their status to and receive configuration from a
Server and to receive agent package updates from the server.
The protocol is vendor-agnostic, so the Server can remotely monitor and
manage a fleet of different Agents that implement OpAMP, including a fleet of
mixed agents from different vendors.

This repository is work-in-progress of an OpAMP implementation in Go.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

Approvers ([@open-telemetry/opamp-go-approvers](https://github.com/orgs/open-telemetry/teams/opamp-go-approvers)):

- [Alex Boten](https://github.com/codeboten), Lightstep
- [Anthony Mirabella](https://github.com/Aneurysm9), AWS
- [Evan Bradley](https://github.com/evan-bradley), Dynatrace
- [Srikanth Chekuri](https://github.com/srikanthccv), signoz.io

Emeritus Approvers

- [Przemek Maciolek](https://github.com/pmm-sumo), Sumo Logic

Maintainers ([@open-telemetry/opamp-go-maintainers](https://github.com/orgs/open-telemetry/teams/opamp-go-maintainers)):

- [Andy Keller](https://github.com/andykellr), observIQ
- [Tigran Najaryan](https://github.com/tigrannajaryan), Splunk

Learn more about roles in the [community repository](https://github.com/open-telemetry/community/blob/main/community-membership.md).

Thanks to all the people who already contributed!

<a href="https://github.com/open-telemetry/opamp-go/graphs/contributors">
  <img src="https://contributors-img.web.app/image?repo=open-telemetry/opamp-go" />
</a>
