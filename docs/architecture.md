# ORB architecture

This page explains how ORB is put together, for contributors. What ORB must
do is defined in [requirements.md](../requirements.md); requirement IDs are
given in brackets.

## Components

```
 orbctl ──(Kubernetes API: CRDs, service proxy)──▶ orb-controller ──▶ SQLite (PVC)
                                                     ▲   │ creates
                                agent API (HTTP/JSON)│   ▼
                                                   agent pods (Indexed Job)
                                                     │
                                                     ▼ OCI Distribution API
                                               registry under test
```

| Component | Binary | Package(s) |
|---|---|---|
| CRD types `orb.kubectl.dk/v1alpha1` | – | `api/v1alpha1` |
| Controller (operator) | `orb controller` | `internal/controller`, `internal/store`, `internal/export` |
| Agent | `orb agent` | `internal/agent`, `internal/scenario/...`, `internal/imageset` |
| CLI | `orbctl` | `internal/cli` |
| Agent ↔ controller messages | – | `internal/protocol` |
| Measurement | – | `internal/metrics`, `internal/loadprofile` |
| OCI client and registry modules | – | `internal/registry/core`, `internal/registry/{generic,harbor}`, `internal/registry/modules` |

The `orb` image contains both the controller and the agent. The controller
starts agents with its own image.

## Run lifecycle

The controller reconciles `OrbBenchmarkRun` objects. Phases follow RUN-5:

1. **Pending** – the controller loads the `OrbBenchmark`, applies overrides,
   calls `Default()` and `Validate()`, and stores the snapshot in
   `status.benchmarkSpec` (RUN-3). It creates the agent token Secret and the
   agent Indexed Job, and adds a finalizer.
2. **Preparing** – each agent fetches the plan, builds its registry clients
   and downloads the static image set if there is one (IMG-6). Agent 0 also
   runs the registry checks (REG-7) and resolves static image digests
   (IMG-8). Each agent reports `ready`. If a check fails, agent 0 reports
   `failed` with the check results instead. A `checkOnly` run skips the
   static image download and ends here as `Succeeded` (or `Failed` if a
   check failed).
3. **Seeding** – the controller tells all agents to `seed`. Seeding is
   sharded: pool item `k` of each scenario is pushed by agent `k % agents`.
   Each agent reports `seeded`.
4. **Running** – for each scenario in order (TYPE-2) the controller sets a
   shared start time a few seconds in the future and sends `run` (LOAD-3).
   Every agent first resolves what it needs from the seeded content (for
   example pool manifests; not measured), waits for the start time, runs the
   stage plan, sends interval reports, and reports `scenarioDone`.
5. **Collecting** – the controller builds the `OrbBenchmarkResult` from its
   merged histograms and the SQLite data.
6. **CleaningUp** – if the cleanup policy is `Delete`, agent 0 deletes the
   run's repositories through the registry module (CLEAN-1..5).
7. **Succeeded / Failed / Aborted** – agents are told to `exit`.

Setting `spec.abort: true` makes the controller answer `abort`. Agents stop
their workers, send a final report and report `aborted`. The controller
writes the result marked as aborted (RUN-6).

If an agent pod fails, or an agent reports `failed`, the run fails. The
coordination state lives in the controller's memory: if the controller
restarts during a run, that run is marked `Failed`.

## Agent API

Agents poll the controller over HTTP. Paths and messages are defined in
`internal/protocol`. Every request carries `Authorization: Bearer <token>`,
where the token comes from the run's agent token Secret.

| Request | Purpose |
|---|---|
| `GET  /agent/v1/runs/{uid}/plan` | the `Plan` (spec snapshot, agent count, repository root) |
| `GET  /agent/v1/runs/{uid}/agents/{i}/command` | what to do next (`Command`); polled every 500 ms |
| `POST /agent/v1/runs/{uid}/agents/{i}/state` | a finished step (`StateUpdate`) |
| `POST /agent/v1/runs/{uid}/agents/{i}/report` | one `IntervalReport` per measurement interval |
| `GET  /export/v1/runs/{uid}` | detail data as JSON (`protocol.Detail`), or `?format=sqlite` |

Commands per controller phase:

| Phase | Agent 0 | Other agents |
|---|---|---|
| Preparing | `wait` | `wait` |
| Seeding | `seed` until it reports `seeded`, then `wait` | same |
| Running scenario *k* | `run` (scenario *k*, start time) until it reports `scenarioDone` for *k*, then `wait` | same |
| CleaningUp | `cleanup` | `exit` |
| finished | `exit` | `exit` |
| abort requested | `abort` | `abort` |

## Agent pod contract

The controller creates one Indexed Job per run, named `<run>-agents`, with
`completions = parallelism = agents` and `backoffLimit: 0`. Pods carry the
label `orb.kubectl.dk/run: <run name>`.

| Environment variable | Meaning |
|---|---|
| `ORB_CONTROLLER_URL` | base URL of the agent API, for example `http://orb-controller.orb-system.svc:8082` |
| `ORB_RUN_UID` | the run UID |
| `ORB_RUN_NAME` | the run name |
| `ORB_AGENT_TOKEN` | the agent token (from Secret `<run>-agent-token`, key `token`) |
| `JOB_COMPLETION_INDEX` | the agent index (set by Kubernetes) |

| Mount | Content |
|---|---|
| `/etc/orb/registry/.dockerconfigjson` | registry credentials (`registry.credentialsSecret`), if set |
| `/etc/orb/source/.dockerconfigjson` | static image source credentials, if set |
| `/etc/orb/ca/ca.crt` | extra CA certificates (`registry.caSecret`), if set |
| `/scratch` | emptyDir for the static image set (size limit from `storageLimit`, medium from `storageMedium`) |

Secrets never pass through the controller (AUTH-6).

## Repository layout

Each run pushes below `<repositoryPrefix>/<run name>` (IMG-12), called the
repository root.

| Repository | Used for |
|---|---|
| `<root>/<scenario>/pool` | seeded images for pull, HEAD, tag and tag listing; tags `img-<k>` |
| `<root>/<scenario>/push` | load-scenario pushes; tags `a<agent>-w<worker>-<n>` |
| `<root>/<scenario>/source` | deduplication: the existing content, tags `img-<k>` |
| `<root>/<scenario>/target` | deduplication pushes (scope `SameProject`) |
| `<targetProject>/<run name>/<scenario>/target` | deduplication pushes (scope `DifferentProject`) |

With scope `SameRepository`, deduplication pushes go to the source repository.
Deduplication pushes use tags `a<agent>-w<worker>-<n>` and baseline pushes tags
`b-a<agent>-w<worker>-<n>` in the target repository. Tagging (OP-5) adds tags
`t-a<agent>-w<worker>-<n>` in the pool repository.

## Measurement

Each worker records a `metrics.Result` per operation. The agent's
`metrics.Recorder` turns results into one `protocol.IntervalReport` per
measurement interval, with HDR histograms in compressed V2 encoding. The
interval number counts from the scenario's shared start time.

Operation names in results: `pull`, `push`, `headManifest`, `headBlob`,
`tag`, `listTags`, `catalog`, and for deduplication `existenceCheck`,
`crossRepoMount`, `reupload` and `baseline`.

Latency definitions (section 8 of the requirements):

| Operation | Latency ends at |
|---|---|
| pull | first byte of the first blob body (the config blob, then layers) |
| push | `202` for the first PATCH of the first blob |
| headManifest, headBlob | first byte of the response |
| listTags, catalog | first byte of the first page (total time in `total`) |
| tag | response to the manifest PUT |
| deduplication methods and baseline | response to the manifest PUT |

Failed operations are counted per error type but left out of latency
histograms. Operations cut short by an abort are not recorded. Cleanup
problems are reported as a cleanup message, never as a failed run (CLEAN-4).

The controller merges histograms (never averages percentiles, RES-2) and
stores every report in SQLite:

| Table | Content |
|---|---|
| `runs` | one row per run |
| `intervals` | per run, scenario, interval and agent: stage, warm-up flag, workers, CPU, scheduler delay |
| `operations` | per run, scenario, interval, agent and operation: count, errors, bytes |
| `histograms` | per run, scenario, interval, agent, operation and metric: the encoded histogram |
| `counters` | per run, scenario, interval, agent, operation and name: error types and extra counters |
| `error_examples` | per run, scenario, operation and error type: one example message |
| `samples` | raw samples, if enabled (RES-9) |

## Saturation (LOAD-9)

Each interval, agents measure their CPU utilization (process CPU time from
`getrusage`, relative to `GOMAXPROCS`, which follows the container's CPU
limit) and the 99th percentile Go scheduler latency (runtime metric
`/sched/latencies:seconds`). The
controller flags a run as saturated when, outside warm-up, more than 10% of
agent intervals show CPU utilization above 90% or a scheduler delay p99
above 50 ms.
