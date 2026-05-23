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

## Deployment strategies

- [Deployment strategies](docs/DEPLOYMENT_STRATEGIES.md) — what `AppDeployDriver`, `AppBlueGreenDriver`, and `AppCanaryDriver` actually do on DO App Platform, including the in-rollout availability probe and the InstanceCount<2 single-instance non-guarantee.

## Requirements

- workflow engine ≥ `0.57.1`
- `DIGITALOCEAN_TOKEN` environment variable set to a valid DO personal access token

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).
