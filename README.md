# workflow-plugin-digitalocean

> ✅ **Verified** — used in production at **buymywishlist, core-dump, workflow-compute**. This plugin has been validated end-to-end in a merged main-branch wfctl.yaml of an active GoCodeAlone project.

DigitalOcean IaC provider for the [GoCodeAlone/workflow](https://github.com/GoCodeAlone/workflow) engine. Manages App Platform, App Platform domains, DOKS, databases, Redis cache, load balancers, VPC, firewall, DNS, Spaces, DOCR, certificates, Droplets, Block Storage volumes, IAM (declared), and API gateway resources via `wfctl infra`.

## Supported resource types

| Type | Description |
|------|-------------|
| `infra.container_service` | DigitalOcean App Platform service |
| `infra.app_domain` | App Platform domain binding |
| `infra.k8s_cluster` | DigitalOcean Kubernetes (DOKS) |
| `infra.database` | Managed database (PostgreSQL, MySQL, Redis, MongoDB) |
| `infra.cache` | Managed Redis cache |
| `infra.load_balancer` | Load balancer |
| `infra.vpc` | Virtual Private Cloud |
| `infra.firewall` | Cloud firewall (Droplet/DOKS tag-based) |
| `infra.dns` | DNS domain, records, and targeted stale-record removal |
| `infra.storage` | Spaces object storage |
| `infra.registry` | DigitalOcean Container Registry (DOCR) |
| `infra.certificate` | TLS certificate |
| `infra.droplet` | Droplet (VM) |
| `infra.volume` | Block Storage volume |
| `infra.iam_role` | IAM role (declarative) |
| `infra.api_gateway` | API gateway |

## Quick start

See [`examples/minimal/config.yaml`](examples/minimal/config.yaml) for a minimal working configuration.

```sh
wfctl infra plan   --env staging
wfctl infra apply  --env staging
```

## App Platform outputs

`infra.container_service` reads expose App Platform ingress and deployment
snapshot fields through `wfctl infra refresh-outputs` and `wfctl infra
outputs`. Hosts should use these provider-owned outputs for staging URL and
readiness decisions instead of calling DigitalOcean App Platform APIs directly.

Key output fields include:

| Output | Description |
|--------|-------------|
| `live_url` | App Platform live URL. |
| `default_ingress` | Default App Platform ingress URL. |
| `active_deployment_id`, `active_deployment_phase` | Current active deployment slot when present. |
| `in_progress_deployment_id`, `in_progress_deployment_phase` | Current in-progress deployment slot when present. |
| `pending_deployment_id`, `pending_deployment_phase` | Current pending deployment slot when present. |
| `active_deployment_image_refs` | Active services/workers mapped by component name to canonical image refs. |

## App Platform workers

For long-running background processes that should run with an App Platform
service, declare them as nested `workers` on the parent `infra.container_service`.
Workers are for outbound/background work. They do not expose an inbound port, so
do not use a worker for a process that another component must dial directly.

```yaml
modules:
  - name: app
    type: infra.container_service
    config:
      provider: do-provider
      name: example-app
      image: registry.digitalocean.com/acme/web:${IMAGE_SHA}
      http_port: 8080
      env_vars:
        QUEUE_NAME: image-jobs
      workers:
        - name: example-worker
          image: registry.digitalocean.com/acme/worker:${IMAGE_SHA}
          instance_count: 1
          env_vars:
            QUEUE_NAME: image-jobs
```

If another component must connect to the process over the app's private network,
model that process as an internal service instead:

```yaml
modules:
  - name: internal-broker
    type: infra.container_service
    config:
      provider: do-provider
      name: internal-broker
      image: registry.digitalocean.com/acme/broker:${IMAGE_SHA}
      expose: internal
      internal_ports:
        - 4222
```

Sibling services and workers in the same DigitalOcean App can reach an internal
service on its declared internal port. Standalone background workloads that do
not need an in-App web sibling should still be modeled deliberately; this plugin
does not expose a separate worker-only resource type today.

## Database outputs

`infra.database` reads expose lifecycle status through `wfctl infra
refresh-outputs` and `wfctl infra outputs`. Hosts can use the `status` output
(`online` for healthy managed databases) without calling DigitalOcean database
APIs directly.

## DNS stale-record removal

`infra.dns` is not authoritative for every record in a zone. Use `absent_records` to delete specific stale records while leaving unmanaged records intact.

```yaml
resources:
  - name: site-dns
    type: infra.dns
    config:
      domain: example.com
      absent_records:
        - type: CNAME
          name: www
          data: example.com.
```

`data` is optional. When omitted, every record matching `type` and `name` is deleted. When set for hostname-like records such as `CNAME`, `MX`, `NS`, and `SRV`, matching ignores case and a trailing dot.

DNS reads and imports preserve a provider-neutral `authority` output alongside the legacy `authoritative_nameservers` list:

```json
{
  "authority": {
    "role": "authoritative_dns",
    "dns_host": "DigitalOcean",
    "name_servers": ["ns1.digitalocean.com", "ns2.digitalocean.com", "ns3.digitalocean.com"]
  }
}
```

The nested shape matches Workflow DNS replay fixtures while keeping existing flat outputs stable for current consumers.

## Deployment strategies

- [Deployment strategies](docs/DEPLOYMENT_STRATEGIES.md) — what `AppDeployDriver`, `AppPrevalidatedRollingDriver` / legacy `AppBlueGreenDriver`, and `AppCanaryDriver` actually do on DO App Platform, including the in-rollout availability probe, the InstanceCount<2 single-instance non-guarantee, and when true front-door blue/green requires DO Load Balancer + Droplets or an external proxy.

## Requirements

- workflow engine ≥ `0.57.1`
- `DIGITALOCEAN_TOKEN` environment variable set to a valid DO personal access token

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).
