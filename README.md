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
| `infra.dns` | DNS domain + records |
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

## Requirements

- workflow engine ≥ `0.57.1`
- `DIGITALOCEAN_TOKEN` environment variable set to a valid DO personal access token

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).
