# warden

An Ansible execution proxy that keeps the local development loop.

> **Status: working prototype.** The execution path is proven end to end against
> a real cluster, callers authenticate for real, and access to an environment is
> granted rather than assumed. **The network perimeter is still missing** — see
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

## Signing in

```sh
warden login --subject alice    # password read from the terminal
warden whoami                   # asks the SERVER who it thinks you are
warden logout
```

The session token is stored in `~/.config/warden/credentials.json` at mode 0600,
**keyed by server address** — you talk to a dev warden and a prod one, and
overwriting one with the other would point a command at the wrong fleet. The
permissions are checked on *read*, not just set on write: a permissive umask or a
`cp` without `-p` loosens them and nothing would otherwise say so.

Passwords are argon2id, in a table `warden_reader` has no grant on — an audit
reader can name actors without being able to attack their passwords offline. Only
the SHA-256 of a session token is stored, never the token.

Changing a password revokes every session in the same transaction, and so does
disabling an account. Doing one without the other would mean cutting an access
that keeps working for up to thirty days.

The first account is created from the cluster, because its authorisation is
database access rather than a password — the only one that exists before any
account does:

```sh
kubectl exec deploy/warden-api -- wardend user add --subject alice --admin
```

The password is **generated by the server**, shown once. A password chosen by an
administrator is a password that administrator knows, and it is almost always
weak because it has to be dictated.

---

## Who may run where

Access to an environment is granted, never assumed — and **there is no command
that grants it**. The source is a ConfigMap in the deployment repo, reconciled by
the `controller`:

```yaml
# charts/warden/values.yaml, or your own values file
grants:
  environments:
    prod:
      - alice
      - bob
    staging: []      # explicitly nobody
```

Changing access means committing. `warden grant list` shows the result, including
revoked grants — it is an audit view. There is no write path at all: an API that
could grant would be a second source of truth, silently overwritten on the next
pass, and a grant that vanishes a minute after you made it is worse than a
missing command.

**Removing someone from the file revokes their access.** For every environment
the file names, the list is authoritative. Measured on the reference deployment:
about a minute from `kubectl apply` to effect — dominated by the kubelet
republishing the mounted ConfigMap, not by warden.

Grants and pods are reconciled on **separate clocks**, and that split matters: a
pod pass costs a LIST against the Kubernetes API and only bounds *audit* lag,
while a grant pass costs a file read and bounds how long a *revocation* takes to
bite. On one clock you could not speed up the second without paying for the
first. Defaults: 30 s for pods, 10 s for grants.

Four things it refuses to do, because a mechanism that can cut production access
should fail on the safe side:

- a **missing** file changes nothing — a controller started before the ConfigMap
  exists must not conclude "nobody has access";
- an **empty** document is refused and logged. An empty list for a *named*
  environment, though, is an explicit "nobody" and does revoke;
- an environment the file does **not name** is left alone, so grants split across
  files and a truncated one cannot revoke everything;
- applying is **all-or-nothing** — a typo in an account name fails the whole
  document rather than revoking people without granting anyone.

What this buys, stated exactly, because a control whose reach is overestimated is
worse than no control:

- **it stops** obtaining the SSH key of an environment you have no grant for. No
  key means no machine is reachable at all.
- **it does not stop** a legitimate `staging` run from touching a `prod` box.
  Egress is open, and §10.2 is explicit that a perimeter is enforced by the
  network or it is not enforced.

The check runs twice: at run creation, and again when the order is delivered.
The second is what gives revocation teeth — a pod that has not yet received its
key will not get it.

Being an admin grants nothing implicitly. An admin who wants `prod` grants it to
themselves, and that leaves a dated, signed row — so "who could reach prod" has
one answer, from one table.

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

The shared secret comes first — the chart creates no Secret, because a secret in
a git repository is a published secret:

```sh
kubectl create namespace warden-dev
kubectl create secret generic warden-internal -n warden-dev \
  --from-literal=internal-token="$(openssl rand -base64 32)" \
  --from-literal=forge-token="<forge read token>"

make images-push
helm install warden charts/warden -n warden-dev \
  --set ingress.api.host=warden.example.com \
  --set ingress.git.host=warden-git.example.com
```

Migrations run as a `pre-install,pre-upgrade` hook, so the schema is up to date
*before* the new binary starts on it — ArgoCD reads the same annotations as
PreSync. Ordering is no longer something you have to remember.

See [`charts/warden/README.md`](charts/warden/README.md) for the details,
including what the chart deliberately does **not** make configurable, and why.

---

## What is not there yet

Stated plainly, because a product that overstates its guarantees is worse than
one that has none.

**No network perimeter.** Runner egress is wide open
(`charts/warden/templates/networkpolicy.yaml`). Environment grants ([ADR-0033]) stop you from
*obtaining* an environment's key, which is real — no key, no reachable machine.
They do not stop a legitimate `staging` run from touching a `prod` box. §10.2 is
explicit that a perimeter is enforced by the network or it is not enforced, and
this cluster's CNI (kube-router) has no FQDN egress. **This is the single most
important gap.**

**No SSO.** Authentication is local accounts with passwords ([ADR-0032]) — real,
verified, but the account directory is warden's own. OIDC and mTLS remain to be
written, and until then there is no central place to revoke a person.

**No account lockout.** Repeated failures are slowed — a 250 ms floor and at most
four concurrent hashes, bounding attempts to roughly sixteen a second — but a
password is never locked out. Lockout is itself a denial-of-service lever against
a named user, so the rate bound was chosen instead.

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

The section above is the short, honest version for someone deciding whether to
use warden. The exhaustive working checklist — every open item with the file and
line it lives at — is [`docs/reste-a-faire.md`](docs/reste-a-faire.md), in
French like the ADRs.

---

## Design decisions

34 ADRs in [`docs/adr/`](docs/adr/), in French, each with the options rejected
and why. They exist so that a settled question is not re-decided backwards six
months later. The ones that explain the most:

- [ADR-0005] — the git binary as the single engine
- [ADR-0007] — how a runner reaches the code, scoped to one SHA
- [ADR-0008] — PostgreSQL as both journal and event transport, no bus
- [ADR-0011] — one binary per trust domain, boundaries enforced by the compiler
- [ADR-0020] — access credentials, and what v1 knowingly does not do
- [ADR-0030] — machine enrolment, and the egress we opened
- [ADR-0031] — reconciling killed runs, and who may pronounce an outcome
- [ADR-0032] — local accounts and sessions, and what a login endpoint refuses to say
- [ADR-0033] — environment grants: the half of authorization that does not need the network

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
[ADR-0032]: docs/adr/0032-comptes-locaux-et-sessions.md
[ADR-0033]: docs/adr/0033-droits-par-environnement.md
