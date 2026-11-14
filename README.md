# ORB — OCI Registry Benchmark

[![CI](https://github.com/zensonic/oci-reg-benchmark/actions/workflows/ci.yml/badge.svg)](https://github.com/zensonic/oci-reg-benchmark/actions/workflows/ci.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/zensonic/oci-reg-benchmark/badge)](https://scorecard.dev/viewer/?uri=github.com/zensonic/oci-reg-benchmark)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

ORB benchmarks OCI container registries that you operate yourself, with Harbor
as the primary target. It measures push and pull latency and throughput, plus
HEAD, tagging, tag listing, catalog and deduplication performance, under load
from hundreds of simultaneous simulated clients.

ORB runs in Kubernetes as an operator and is controlled with the `orbctl` CLI.

> **Status:** design phase. The requirements are complete; the implementation
> has not started yet. See [requirements.md](requirements.md).

## Installation

Releases are published here:

| Artifact | Location |
|---|---|
| `orb` container image (controller and agent) | `ghcr.io/zensonic/oci-reg-benchmark/orb` |
| Helm chart | `oci://ghcr.io/zensonic/oci-reg-benchmark/charts/orb` |
| `orbctl` binaries (Linux, macOS, Windows) | [GitHub Releases](https://github.com/zensonic/oci-reg-benchmark/releases) |

All release artifacts are signed with [Sigstore cosign](https://docs.sigstore.dev/)
(keyless) and come with SBOMs and SLSA build provenance. To verify an image:

```sh
cosign verify ghcr.io/zensonic/oci-reg-benchmark/orb:<version> \
  --certificate-identity-regexp '^https://github.com/zensonic/oci-reg-benchmark/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Building from source

Requires Go (the version in [go.mod](go.mod)), a C compiler (the `orb` binary
uses CGO for SQLite) and, for images, Docker.

```sh
make build   # builds bin/orb and bin/orbctl
make ci      # runs the same checks as the CI pipeline
```

## Contributing

Contributions are welcome. Please read [CONTRIBUTING.md](CONTRIBUTING.md) first.
All commits must be signed off under the
[Developer Certificate of Origin](https://developercertificate.org/).

## License

ORB follows the intellectual property policy in
[section 11 of the CNCF Charter](https://github.com/cncf/foundation/blob/main/charter.md#11-ip-policy):

- Code is licensed under the [Apache License 2.0](LICENSE).
- Documentation is licensed under
  [Creative Commons Attribution 4.0 International (CC BY 4.0)](https://creativecommons.org/licenses/by/4.0/).

Copyright The ORB Authors.
