# warden

An Ansible execution proxy that keeps the local development loop.

> **Status: working prototype.** The execution path is proven end to end against
> a real cluster. **Authentication and authorization are not implemented** — see
> [What is not there yet](#what-is-not-there-yet) before drawing conclusions.

---

## The problem

Ansible is an excellent execution engine and a poor control point: anyone with
the binary and an SSH key can reach any machine, leaving no usable audit trail
and enforcing no authorization.

AWX, Semaphore and Rundeck solve audit and authorization — but they require the
code to come from a **synchronized SCM**. In practice: commit → push → sync the
project → launch. The development loop goes from five seconds to several
minutes, which pushes engineers straight back to an SSH bastion. A control point
people route around controls nothing.

## The idea

**Use git as the transport, and the same commit as the audit record.**

`warden submit` builds a commit from your working tree — *including uncommitted
work* — without touching your index or `HEAD`, pushes it to the server, and the
runner executes **that exact commit**. Git deduplicates by blob, so only the
delta travels.

Measured on the reference deployment: **4 objects pushed, 12 seconds** from
submit to a finished run that modified a remote machine.

Two properties fall out of this, and they are the whole point:

- **What executed is what was audited.** Not a description of it — the commit
  itself, identified by its SHA, still present months later.
- **The loop stays fast.** No commit, no push to a forge, no project sync.

---

## How a run works

```
  your machine                control plane                  runner pod
  ───────────                 ─────────────                  ──────────
  warden submit
    │
    ├─ build audit commit ────────────────────────────────┐
    │  (GIT_INDEX_FILE, commit-tree — your repo untouched)│
    │                                                      │
    ├─ push ──────────────────▶ git role                   │
    │                           (pre-receive: freshness,   │
    │                            write-once audit ref)     │
    │                                                      │
    └─ POST /runs ────────────▶ api role                   │
                                  │                        │
                                  ├─ record the run        │
                                  └─ launch pod ──────────▶ starts
                                                            │
                                     dispatch (SSE) ───────▶ receives the order
                                                            │  (git token, SSH key)
                                     git read ◀────────────── fetch, scoped to
                                                            │  one ref by
                                                            │  GIT_NAMESPACE
                                                            ├─ ansible-runner
                                                            │  drives the playbook
                                     ingest ◀────────────── events, batched,
                                                            │  as they happen
                                     ingest ◀────────────── final frame: the run
                                                               is closed
  warden follows the WebSocket stream throughout
```

Three things about this flow are deliberate and easy to miss:

**The pod never names its own run.** It presents a bootstrap token; the server
resolves `pod → run` from its own database. A run id in a request body is never
trusted — it is a sequential integer, therefore guessable.

**Events are persisted as they happen, never buffered.** The question that
matters when a run is killed mid-flight is *what had already changed*, and a
buffer would lose exactly that.

**The closing frame travels with the last batch of events**, in one request.
Two requests would leave a window where the run is closed but its final events
are not recorded.

**A dying runner does not pronounce a verdict.** When the pod is killed under it
— evicted, OOM-killed, or stopped on the run deadline — `ansible-runner` reports
`failed` simply because the play was interrupted. Recording that would say *your
playbook failed* about a playbook that said nothing. The runner flushes its
events and stops there; the `controller` closes the run, because it is the only
component that can see `DeadlineExceeded` or `Evicted` ([ADR-0031]).

Measured on the reference deployment: a run killed mid-flight is closed 6 seconds
later, keeping the change it had already made.

---

## Machine enrolment

A run needs an SSH key on the target. warden offers two ways, converging on the
same inventory entry:

**Declarative** — you put the public key on your machines yourself, then tell
warden about them:

```sh
warden key generate --env prod     # warden mints the pair; the private half
                                   # never leaves the cluster
warden key show --env prod         # prints the line to add to authorized_keys
warden host declare -f hosts.yaml
```

```yaml
hosts:
  - host: db-prod-02        # the name as your Ansible inventory calls it
    address: 10.20.0.42     # an ADDRESS: this is where we connect
    login: ansible
    host_key: "ssh-ed25519 AAAA..."   # optional — and it is what protects
                                      # you from a blind first contact
```

**Assisted** — warden connects and installs the key itself. You supply a
login and a password, once:

```sh
warden enroll --env prod --host db-prod-02 --address 10.20.0.42 --login ansible
```

The password is read from the terminal (never `argv`, never the environment),
travels through the SSE channel, and is destroyed when read. warden connects,
records the host key, appends its public key idempotently, then **reconnects
using the key** — without that check you would register a machine no run can
actually reach.

The fingerprint it observed is then shown to you for approval before the machine
is recorded. This is the only moment where a blind first contact can still be
refused, so the prompt names the exact file to compare against and **defaults to
no**.

Once a machine is pinned, a substituted host is refused outright — a distinct
error from a network failure, because the correct response is to investigate,
not to retry.

An address is required and a hostname will not do: resolving a name inside the
pod could enrol a machine you did not mean to designate, and a machine enrolled
at the wrong address is a machine you only *believe* you can reach.

---

## Architecture

### Three trust domains, three binaries

The split is enforced by the compiler, not by review ([ADR-0011]):

| Binary | Runs on | Trust |
|---|---|---|
| `warden` | your machine | Nothing it computes is authoritative. The effective config is resolved server-side from the commit. |
| `wardend` | the cluster | Holds the policy, the audit log, the credentials. |
| `warden-runner` | inside the pod | **Hostile by construction.** It runs arbitrary code — your playbook. Its image contains no control-plane code. |

`internal/runner` cannot import `internal/server/internal/...`. That is a build
error, not a convention.

### One binary, four server roles

`wardend <role>` ([ADR-0006]). Deployed separately, so the network policy can be
expressed per role:

| Role | Port | Purpose |
|---|---|---|
| `api` | 8080 | HTTP API, WebSocket stream, administration |
| — | 8084 | dispatch (SSE) — the only surface that hands out credentials |
| — | 8085 | ingest — events coming back from pods |
| `git` | 8081 | internal API (diffs, mirrors), reachable only by `api` |
| — | 8082 | smart HTTP **write**, for the CLI |
| — | 8083 | smart HTTP **read**, for runner pods |
| `ingest` | 8085 | the ingest endpoint alone — implemented, not deployed |
| `controller` | — | reconciles runs against what the cluster shows; serves nothing |

Ports rather than paths, because that is what a `NetworkPolicy` can express.

### Hexagonal core

`internal/server/internal/core` knows no infrastructure — no database, no
Kubernetes, no HTTP ([ADR-0019]). Ports are declared where they are *used*;
adapters satisfy them without naming them. This is what makes most of the
product testable without a cluster.

### git is a component, not a tool

warden shells out to the `git` binary ([ADR-0005]) and relies on plumbing that
was measured, not assumed: `GIT_NAMESPACE` for read isolation,
`GIT_QUARANTINE_PATH` for the write edge, a `pre-receive` hook reached through a
symlink and dispatched on `argv[0]`.

Where measurements contradicted the documentation, the measurement won and a
probe test was left behind to catch the day it changes.

---

## Testing

Three stages ([ADR-0010]), and the boundary between them is a property worth
keeping:

```sh
make test              # stage 1 — no Docker, no cluster. Stays fast.
make test-integration  # stage 2 — real PostgreSQL, real sshd, real git
make test-runner-image # ansible-runner inside the image that ships
make test-e2e          # stage 3 — a real cluster, disposable namespace
```

A stage-1 test that starts needing Docker has slipped a stage.

**Doubles do not find everything, and that is the lesson of this codebase.** Six
defects survived green stage-1 and stage-2 suites and were found only by
deploying: an uppercase ULID rejected as a Kubernetes Secret name (the fake
clientset accepts it), a stream end frame hardcoded to `succeeded`, an empty
label rejected by the API, a security prompt naming the wrong host-key file,
`/tmp` read-only breaking `ssh-agent`, and an audit column recording a lie.

Each came from a real component with opinions of its own.

---

## Running it

Requires Go 1.26, git ≥ 2.45, Docker, and a Kubernetes cluster.

```sh
make doctor            # checks your environment and says what is missing
```

Deployment order matters — migrations carry schema changes the new binary
depends on:

```sh
kubectl apply -f deploy/00-namespace.yaml
kubectl create secret generic warden-internal -n warden-dev \
  --from-literal=internal-token="$(openssl rand -base64 32)" \
  --from-literal=forge-token="<forge read token>"

make images-push
# run the migrate Job, then:
kubectl apply -f deploy/
```

See [`deploy/README.md`](deploy/README.md) for the details, including what this
deployment deliberately is *not*.

---

## What is not there yet

Stated plainly, because a product that overstates its guarantees is worse than
one that has none.

**No authorization.** Runner egress is currently wide open
(`deploy/60-networkpolicy.yaml`). The §10.2 perimeter — the only real
authorization control in v1 — does not exist. Any run can reach any machine with
its environment's key. This is the single most important gap.

**No authentication.** `WARDEN_INSECURE_DEV_AUTH=1` trusts an HTTP header. The
audit says "alice" because a client claimed to be alice. OIDC and mTLS remain to
be written.

**No leader election.** The `controller` role runs at one replica and relies on
its actions being idempotent rather than on a Lease ([ADR-0031]). Two
controllers would duplicate work, not corrupt it — but the Lease the ADR-0006
describes is still unwritten.

**One key per environment.** Every machine in `prod` shares one key. The host's
logs will say `ansible`, not `alice` — non-repudiation stops at the product
boundary ([ADR-0020]). Rotation is the only revocation mechanism and it is not
tooled: replacing a key does **not** remove the old one from any
`authorized_keys`.

**Trust on first use.** Without a declared `host_key`, the first enrolment
trusts whatever answers. The fingerprint is captured, displayed and confirmed —
but that first contact is not protected. Prefer `warden host declare` with
`host_key` in production.

**Not measured:** event volume per run, behaviour at scale, warm-pool
prefetching (§8.4).

---

## Design decisions

32 ADRs in [`docs/adr/`](docs/adr/), in French, each with the options rejected
and why. They exist so that a settled question is not re-decided backwards six
months later. The ones that explain the most:

- [ADR-0005] — the git binary as the single engine
- [ADR-0007] — how a runner reaches the code, scoped to one SHA
- [ADR-0008] — PostgreSQL as both journal and event transport, no bus
- [ADR-0011] — one binary per trust domain, boundaries enforced by the compiler
- [ADR-0020] — access credentials, and what v1 knowingly does not do
- [ADR-0030] — machine enrolment, and the egress we opened
- [ADR-0031] — reconciling killed runs, and who may pronounce an outcome

The product specification lives in [`specs/initial.md`](specs/initial.md).

[ADR-0005]: docs/adr/0005-binaire-git-comme-moteur-unique.md
[ADR-0006]: docs/adr/0006-un-binaire-trois-roles.md
[ADR-0007]: docs/adr/0007-acces-du-runner-au-code.md
[ADR-0008]: docs/adr/0008-postgresql-comme-journal-et-transport-devents.md
[ADR-0010]: docs/adr/0010-strategie-de-test-en-trois-etages.md
[ADR-0011]: docs/adr/0011-un-binaire-par-domaine-de-confiance.md
[ADR-0019]: docs/adr/0019-decoupage-hexagonal-du-control-plane.md
[ADR-0020]: docs/adr/0020-key-inventory-credentials-dacces.md
[ADR-0030]: docs/adr/0030-enrolement-des-machines.md
[ADR-0031]: docs/adr/0031-reconciliation-des-runs-tues.md
