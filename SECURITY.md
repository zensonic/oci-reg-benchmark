# Security policy

## Reporting a vulnerability

Please **do not** report security vulnerabilities in public issues.

Report them privately through GitHub:
[open a security advisory](https://github.com/zensonic/oci-reg-benchmark/security/advisories/new).

Include a description of the problem, steps to reproduce it, and the affected
version. The maintainers will confirm receipt and keep you updated on the fix.

## Supported versions

Security fixes are made for the latest release.

## Verifying releases

All release artifacts are signed keyless with Sigstore cosign and include SBOMs
and SLSA build provenance. See the [README](README.md#installation) for how to
verify them.
