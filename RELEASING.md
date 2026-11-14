# Releasing ORB

Releases are automated. Pushing a version tag starts the
[release workflow](.github/workflows/release.yml).

## Versioning

ORB uses [Semantic Versioning](https://semver.org/). Tags look like `v1.2.3`.
Tags with a suffix, such as `v1.2.3-rc.1`, are published as pre-releases and
do not move the `latest` image tag.

## Cutting a release

1. Make sure `main` is green in CI.
2. Create and push a signed tag from `main`:

   ```sh
   git checkout main && git pull
   git tag -s v1.2.3 -m "v1.2.3"
   git push origin v1.2.3
   ```

3. Watch the release workflow in the Actions tab. It publishes:
   - `orbctl` archives for Linux, macOS and Windows (amd64 and arm64), with
     checksums, SBOMs and a cosign signature, on the GitHub release,
   - the multi-arch image `ghcr.io/zensonic/oci-reg-benchmark/orb:<version>`,
     signed and with build provenance,
   - the Helm chart `oci://ghcr.io/zensonic/oci-reg-benchmark/charts/orb`
     (once the chart exists).
4. Check the generated release notes and edit them if needed. Notes are grouped
   by pull request labels (see [.github/release.yml](.github/release.yml)).

## First release only

New packages on ghcr.io are private by default. After the first release, open
each package (`orb` and `charts/orb`) under the repository's **Packages**, go to
**Package settings** and set the visibility to **Public**.
