# Contributing to ORB

Thank you for helping out. This page explains how to contribute and the rules
every contribution must follow.

## Before you start

- Read [requirements.md](requirements.md). It is the source of truth for what
  ORB must do. If your change alters a requirement, update requirements.md in
  the same pull request.
- For larger changes, open an issue first so the approach can be discussed.

## Licensing and sign-off (required)

ORB follows the IP policy in
[section 11 of the CNCF Charter](https://github.com/cncf/foundation/blob/main/charter.md#11-ip-policy).

**Code** is licensed under the [Apache License 2.0](LICENSE).
**Documentation** is licensed under
[CC BY 4.0](https://creativecommons.org/licenses/by/4.0/).
By contributing, you agree that your contribution is made under these licenses.

### Developer Certificate of Origin (DCO)

ORB does not use a Contributor License Agreement. Instead, every commit must be
signed off to certify the [Developer Certificate of Origin](https://developercertificate.org/):
you wrote the change, or otherwise have the right to submit it under the
project's license.

Sign off by adding `-s` when you commit:

```sh
git commit -s -m "Add pull latency measurement"
```

This adds a line like the following to the commit message. The name and email
must match the commit author:

```
Signed-off-by: Jane Doe <jane.doe@example.com>
```

A CI check rejects pull requests with commits that are not signed off. To fix
the last commit, run `git commit --amend -s`. To fix several, run
`git rebase --signoff main`.

### License headers

Every source file starts with this header (use the comment style of the file's
language):

```go
// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0
```

Run `make license-headers-fix` to add it to new files. CI checks for it.

### Third-party dependencies

New dependencies must use Apache-2.0 or a license on the
[CNCF allowlist](https://github.com/cncf/foundation/blob/main/policies-guidance/allowed-third-party-license-policy.md),
for example MIT, BSD-2-Clause, BSD-3-Clause or ISC. The full list is in the
`ALLOWED_LICENSES` variable in the [Makefile](Makefile), and CI checks it with
`make dependency-licenses`.

Anything else needs an approved CNCF license exception before it can be added.
Check the [CNCF exceptions list](https://exceptions.cncf.io/) first: some
packages already have an exception that applies to all CNCF projects.

## Development workflow

1. Fork the repository and create a branch from `main`.
2. Make your change. Follow the engineering rules in requirements.md
   (section 17): Go only, readable code, small packages, files under about
   1000 lines.
3. Run `make ci` locally. It runs the same checks as the pipeline.
4. Open a pull request. Fill in the template, including the requirement IDs
   your change implements.
5. A maintainer listed in [MAINTAINERS.md](MAINTAINERS.md) reviews and merges it.

## Commit messages

Write a short summary line in the imperative ("Add HEAD blob operation"),
then a blank line and an explanation if needed. Reference requirement IDs
where relevant.

## Code of conduct

This project follows the [CNCF Code of Conduct](CODE_OF_CONDUCT.md).
