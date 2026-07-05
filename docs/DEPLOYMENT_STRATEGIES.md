# Deployment Strategies (DO App Platform)

`workflow-plugin-digitalocean` exposes three deployment-strategy drivers via the
workflow-engine module interfaces:

- `AppDeployDriver` — in-place rolling
- `AppPrevalidatedRollingDriver` — **prevalidated rolling** registered through
  workflow-engine's legacy `BlueGreenDriver` interface
- `AppCanaryDriver` — partial; `RoutePercent` unsupported

This document describes what each driver actually does, what it guarantees,
and what it does not guarantee.

## `AppDeployDriver` (in-place rolling)

`Update(image)` rewrites the App spec with the new image; DO App Platform's
own RollingUpdate semantics replace running instances. `HealthCheck` polls
until:

1. The target deployment reaches `godo.DeploymentPhase_Active`.
2. Every non-default custom domain on the App spec has `Phase == PHASE_Active`
   in `app.Domains` (the live status array).
3. Every non-wildcard custom domain returns 2xx/3xx on
   `GET https://<domain><readiness path>` within 5s.

**Availability guarantees:**

- For `InstanceCount >= 2`: DO replaces instances rolling. Custom-domain
  HTTPS reachability is verified once the deployment is Active.
- For `InstanceCount < 2`: DO restarts the single instance stop-then-start;
  brief downtime is expected. The in-rollout availability probe will surface
  this in operator logs.

**In-rollout probe.** During `PendingDeploy` / `Deploying`, `HealthCheck`
also probes each non-wildcard custom domain and appends
`; domain probe: X/Y custom domains reachable` to the in-progress error
message. This is observation-only — the gate continues to be "deployment
Active + post-Active domain probe."

## Strategy Selection

Use App Platform rolling deploys when a short single-instance restart is
acceptable or when `InstanceCount >= 2` gives enough DO-managed rolling
availability for the service.

Use App Platform prevalidated rolling when the risky failure mode is "new image
does not boot." The plugin validates a green clone under DO runtime checks
before touching the live app, then rolls the validated image onto the live app.
This is the safest strategy available while the custom domain remains attached
to a single DO App Platform app.

Use a true front-door blue/green design only when the requirement is atomic
traffic switching for a custom domain. That requirement is outside DO App
Platform's native routing model and needs one of these architectures:

1. DigitalOcean Load Balancer plus Droplets, where the balancer is the stable
   public endpoint and deployment swaps the backend Droplet pool.
2. An external front door such as Cloudflare or Fastly, where the proxy owns
   the public custom domain and atomically switches between two backing apps.

Both front-door options require application-specific choices for TLS handoff,
session affinity, connection draining, health probes, rollback, observability,
and stateful workload behavior. This plugin exposes the underlying
`infra.load_balancer` and `infra.droplet` resources, but it does not register a
generic true blue/green deployment driver for App Platform services.

## `AppPrevalidatedRollingDriver` / legacy `AppBlueGreenDriver`

**This driver does not provide true traffic-switching on DO App Platform.**
DO App Platform does not natively support routing a single custom domain to
one of several apps; once attached, a custom domain stays attached to its
app. Switching it to a different app requires DNS / TLS cutover, which is
itself downtime-inducing.

The exported Go type is `AppPrevalidatedRollingDriver`. The legacy
`AppBlueGreenDriver` type and constructors are kept as deprecated aliases while
workflow-engine still exposes the deployment step through `module.BlueGreenDriver`.

What the driver actually does:

1. `CreateGreen(image)` — creates a separate "green" app with the new image.
   Custom `Spec.Domains` are stripped from the cloned spec so the green
   clone does not collide with the live custom domain. Green is reachable
   only at its default `*.ondigitalocean.app` URL.
2. `HealthCheck` (pre-Switch) — verifies the green app deploys cleanly under
   DO's runtime checks.
3. `SwitchTraffic` — calls `Update(greenImage)` on the **blue** app,
   triggering DO's in-place rolling deploy on blue with the validated
   image. Blue's custom domain stays attached the whole time.
4. `HealthCheck` (post-Switch) — same gate as `AppDeployDriver` on blue,
   including the in-rollout availability probe described above.
5. `DestroyBlue` — deletes the **green** clone (the method name is
   misleading; the blue app is never destroyed).

**What this buys you:** the new image is started under DO's runtime checks
in isolation before blue is touched. Failed-to-start images never cause
a blue rollout.

**What this does not buy you:** zero-downtime custom-domain handoff. Same
availability properties as `AppDeployDriver` once the rolling deploy of blue
begins.

For true B/G with zero-downtime custom-domain handoff, use one of the
front-door designs in the strategy selection section above.

## `AppCanaryDriver` (partial)

`CreateCanary` creates a separate canary app (with custom domains stripped,
same as `AppPrevalidatedRollingDriver`). `RoutePercent` returns an explicit
"unsupported on App Platform" error; use DO Load Balancer + Droplets for
canary traffic splitting.

## Operator notes

- `wfctl infra refresh-outputs` followed by `wfctl infra outputs` returns the
  provider-owned App Platform deployment snapshot for `infra.container_service`,
  including `live_url`, `default_ingress`, active/in-progress/pending deployment
  IDs and phases, and active service/worker image refs. Deployment hosts should
  use those outputs for readiness and environment URL publication instead of
  direct App Platform API calls.
- The in-rollout availability probe meaningfully fires on the blue
  post-`SwitchTraffic` re-deploy. On the green/canary clone, no custom
  domains are attached, so the probe is a no-op there.
- The deployment-in-progress error message includes
  `[N/M steps; updated Xs ago]` (when `Deployment.Progress` is populated by
  DO) so `wfctl infra apply --wait` shows whether the rollout is moving.
- The workflow engine still names its compatibility interface
  `module.BlueGreenDriver`. The provider therefore registers the
  prevalidated-rolling implementation through that legacy interface until a
  coordinated engine interface rename lands.
