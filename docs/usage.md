# Using ORB

This guide shows how to install ORB, define a benchmark, run it and read the
results. For how ORB works inside, see [architecture.md](architecture.md).

## 1. Install

ORB runs as an operator in Kubernetes. Install the Helm chart into the
`orb-system` namespace:

```sh
helm install orb oci://ghcr.io/zensonic/oci-reg-benchmark/charts/orb \
  --namespace orb-system --create-namespace
```

Then install `orbctl` from the
[GitHub releases](https://github.com/zensonic/oci-reg-benchmark/releases)
and check that it works:

```sh
orbctl version
```

`orbctl` uses your kubeconfig. The usual `--kubeconfig`, `--context` and
`-n/--namespace` flags work as in kubectl. If the controller runs somewhere
other than `orb-system`, pass `--controller-namespace`.

## 2. Store the registry credentials

ORB reads registry credentials from a Secret of type
`kubernetes.io/dockerconfigjson` in the namespace where you create the
benchmark:

```sh
kubectl create secret docker-registry harbor-credentials \
  --docker-server=harbor.example.com \
  --docker-username='robot$bench+orb' \
  --docker-password='<robot account secret>'
```

See [examples/credentials-secret.yaml](../examples/credentials-secret.yaml)
for the same Secret as YAML.

For Harbor, the project named in `registry.repositoryPrefix` must exist. A
robot account with push and pull permission on that project is enough for
pull, push, HEAD, tag and tag-list operations. Two things need more:

- **Catalog** (`/v2/_catalog`) is only allowed for Harbor system admins.
- **Cleanup** deletes repositories through the Harbor API, which needs
  permission to delete repositories in the project. Without it, the run still
  succeeds and reports that cleanup was not possible.

## 3. Define a benchmark

A benchmark (`OrbBenchmark`) is a reusable definition: the registry, the test
data, one or more scenarios, and the load profile of each scenario. Start
from one of the examples:

| Example | What it does |
|---|---|
| [harbor-load.yaml](../examples/harbor-load.yaml) | all seven operations with random images; ramp up, hold, ramp down |
| [harbor-dedup.yaml](../examples/harbor-dedup.yaml) | deduplication: HEAD-and-skip, cross-repository mount and re-upload, with a baseline |
| [static-images.yaml](../examples/static-images.yaml) | pull-heavy load with real images pinned by digest |

```sh
orbctl benchmark apply -f examples/harbor-load.yaml
orbctl benchmark list
orbctl benchmark get harbor-load
```

`orbctl benchmark apply` checks the definition before sending it, so typos in
field names and operation mixes that do not add up to 100 are reported at once.

## 4. Check the registry

Before generating load, check that ORB can reach the registry from inside the
cluster, that the credentials work, and that the registry supports every
operation the benchmark uses:

```sh
orbctl registry check harbor-load
```

This starts a short check-only run with one agent, prints the results and
deletes the run again. It exits with a non-zero status if a check fails.

## 5. Run the benchmark

```sh
orbctl run start harbor-load --watch
```

Each start creates a new run (`OrbBenchmarkRun`), so a benchmark can be run as
often as you like and every run keeps its own results. The run stores a copy
of the benchmark definition, so later edits to the benchmark do not change
what an earlier run did.

Change selected settings for one run with `--set`:

```sh
orbctl run start harbor-load \
  --set seed=7 \
  --set cleanup=Keep \
  --set stages.load=1m:20,5m:20
```

| Key | Meaning |
|---|---|
| `cleanup=Delete\|Keep` | delete or keep what the run pushed |
| `seed=<number>` | random seed, for reproducible random workloads |
| `repositoryPrefix=<prefix>` | where the run pushes (the Harbor project) |
| `workersPerAgent=<number>` | most workers per agent pod |
| `stages.<scenario>=<duration>:<workers>,...` | replace a scenario's load profile |

Follow, inspect and stop runs:

```sh
orbctl run list
orbctl run status harbor-load-x7k2p
orbctl run watch harbor-load-x7k2p    # exits non-zero if the run fails or is aborted
orbctl run abort harbor-load-x7k2p    # results gathered so far are kept
```

For scripts, `orbctl run start harbor-load -o name` prints only the run name.

A run moves through these phases: `Pending`, `Preparing` (agents start and
download static images), `Seeding` (test images are pushed; not measured),
`Running` (one scenario after another), `Collecting`, `CleaningUp`, and
finally `Succeeded`, `Failed` or `Aborted`.

## 6. Read the results

```sh
orbctl result get harbor-load-x7k2p
orbctl result get harbor-load-x7k2p -o yaml
```

The summary shows, per scenario and operation, the count, errors, operations
per second, latency percentiles and throughput. It also says whether the run
was **saturated**: if the agents themselves ran out of CPU, the results show
the limits of the agents rather than of the registry. Add agents (lower
`workersPerAgent`) or give them more CPU, and run again.

### What the latency numbers mean

Latency is measured from the moment a worker starts an operation, including
any token request, until:

| Operation | Latency ends at |
|---|---|
| pull | the first byte of the first blob body (the config blob, then layers) |
| push | the registry's `202` for the first chunk of the first blob |
| headManifest, headBlob | the first byte of the response |
| listTags, catalog | the first byte of the first page (the total time for all pages is reported too) |
| tag | the response to the manifest PUT |
| deduplication methods and baseline | the response to the manifest PUT |

Throughput is measured from the first to the last byte of each blob, so
latency does not lower it. Failed operations are counted per error type, but
left out of the latency figures.

### Export and compare

The summary is kept in the `OrbBenchmarkResult`. The controller also keeps
full-resolution data per second, per agent and per phase. Export both
together:

```sh
orbctl result export harbor-load-x7k2p -o json   --output run.json
orbctl result export harbor-load-x7k2p -o csv    --output run-csv/
orbctl result export harbor-load-x7k2p -o sqlite --output run.db
```

| Format | Content |
|---|---|
| `json` | one document with `summary` and `detail` |
| `csv` | `summary_operations.csv`, `intervals.csv`, `phases.csv`, `agents.csv`, `errors.csv`, and `samples.csv` if raw samples were recorded |
| `sqlite` | a standalone SQLite database with only this run |

Compare two runs, for example before and after a registry change:

```sh
orbctl result compare harbor-load-x7k2p harbor-load-q9w4m
```

Deleting a run also deletes its result and its detail data.

## 7. Permissions for orbctl users

Users of `orbctl` need these Kubernetes permissions:

| Resource | Verbs | Used for |
|---|---|---|
| `orbbenchmarks.orb.kubectl.dk` | get, list, create, update, delete | `orbctl benchmark` |
| `orbbenchmarkruns.orb.kubectl.dk` | get, list, create, patch, delete | `orbctl run`, `orbctl registry check` |
| `orbbenchmarkresults.orb.kubectl.dk` | get, list | `orbctl result` |
| `services/proxy`, resource name `orb-controller`, in the controller namespace | get | `orbctl result export` |

For example:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: orb-user
  namespace: bench
rules:
  - apiGroups: ["orb.kubectl.dk"]
    resources: ["orbbenchmarks", "orbbenchmarkruns", "orbbenchmarkresults"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: orb-export
  namespace: orb-system
rules:
  - apiGroups: [""]
    resources: ["services/proxy"]
    resourceNames: ["orb-controller"]
    verbs: ["get"]
```

Bind the first Role in every namespace where users run benchmarks, and the
second in the controller namespace.

## Without orbctl

Everything except exporting detail data works with plain kubectl and YAML:
apply an `OrbBenchmark`, create an `OrbBenchmarkRun` with
`spec.benchmarkRef`, and read the `OrbBenchmarkResult`. Detail data can be
fetched with `kubectl port-forward -n orb-system svc/orb-controller 8082` and
`curl http://localhost:8082/export/v1/runs/<run UID>`.
