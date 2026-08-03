# hhq Helm chart

Deploys HappyHome Quest, optionally alongside one or more plugins
(e.g. billtracker-plugin) running as **sidecar containers in the same pod**,
so hhq reaches them over `localhost:<port>` instead of a separate Service.

This chart lives alongside the plain kustomize manifests in `../k8s` - use
whichever deployment method suits your setup. Neither depends on the other.

## Install

From a local checkout:

```sh
helm install hhq deploy/helm/hhq -n hhq --create-namespace -f my-values.yaml
```

Or from the published OCI chart (see the top-level README's "Versioning &
releases" section) - `--version` is required, there's no floating `latest`:

```sh
helm install hhq oci://ghcr.io/mscreations/charts/hhq --version 1.2.0 \
  -n hhq --create-namespace -f my-values.yaml
```

Use `oci://ghcr.io/mscreations/charts/hhq-dev` instead for a dev-channel build.

## Secrets you must create yourself

This chart never templates a Secret with real credentials - only
`existingSecret`-style name references. Create these in the release
namespace before installing (see `../k8s/secret-example.yaml` for the
field/key reference each one needs):

| values.yaml key                        | Default name        | Required keys                                            |
|-----------------------------------------|----------------------|-----------------------------------------------------------|
| `hhq.existingSecrets.app`               | `hhq-app-secrets`    | `ENCRYPTION_KEY` (required); `BOOTSTRAP_PARENT_*` (optional, first-run only) |
| `hhq.existingSecrets.smtp`              | `hhq-smtp`           | `SMTP_HOST`, `SMTP_USER`, `SMTP_PASSWORD`                  |
| `hhq.existingSecrets.db`                | `myapp-postgres-app` | `host`, `port`, `dbname`, `user`, `password` (CNPG shape)  |
| `bootstrap.extraMounts[].secretName`    | (unset)              | One key per file referenced by a `calendars[].password_file` (see below) |
| `plugins[].existingSecret.app`          | (unset)              | Plugin-specific, e.g. `ENCRYPTION_KEY` for billtracker-plugin |
| `plugins[].existingSecret.db`           | (unset)              | Only needed if that plugin's `useHhqDbSecret: false` - `host`, `port`, `dbname`, `user`, `password` (CNPG shape) |

## Bootstrap config files

hhq reads `children.json` / `chores.json` / `assignments.json` /
`calendars.json` / `plugins.json` from `CONFIG_DIR` (default `/config`) on
every startup - see this repo's top-level `CLAUDE.md` for the full field
reference. This chart sources them as follows:

- `bootstrap.children` / `bootstrap.chores` / `bootstrap.assignments` /
  `bootstrap.calendars` - plain YAML lists in `values.yaml`, rendered into a
  ConfigMap. This is safe even for `calendars`, since each entry's password
  should be given as `password_file` (a path into a Secret mounted via
  `bootstrap.extraMounts`, see below) rather than a plain `password` field -
  the same `password`/`password_file` convention billtracker-plugin's
  `bills.json` uses. Setting both on the same entry is a startup-time
  bootstrap error.
- `bootstrap.extraMounts` - mounts one or more existing Secrets (created
  outside this chart, never templated here) into the hhq container, for the
  password files `bootstrap.calendars[].password_file` points at. Same
  shape as a plugin's `extraMounts` (see below).
- `plugins.json` is generated automatically from `.Values.plugins` - you
  never write it by hand.

```yaml
bootstrap:
  calendars:
    - name: Mom's Fastmail
      provider: fastmail
      username: mom@fastmail.com
      password_file: /secrets/calendars/mom-fastmail-password
    - name: Dad's iCloud
      provider: icloud
      username: dad@icloud.com
      password_file: /secrets/calendars/dad-icloud-password
  extraMounts:
    - name: calendars
      secretName: hhq-calendar-passwords
      mountPath: /secrets/calendars
      items:
        - key: mom-fastmail-password
          path: mom-fastmail-password
        - key: dad-icloud-password
          path: dad-icloud-password
```

## Plugin sidecars

Add entries to `plugins:` to run a plugin (e.g. billtracker-plugin) as an
extra container in the same pod as hhq:

```yaml
plugins:
  - name: billtracker
    image:
      repository: ghcr.io/mscreations/billtracker-plugin
      tag: latest
    port: 8090
    useHhqDbSecret: true          # reuse hhq's own DB secret (common case)
    existingSecret: billtracker-app-secrets   # must contain ENCRYPTION_KEY
    bootstrapFiles:
      bills.json: |
        [{"name": "Electric", "amount": 128.43, "schedule": "monthly", "day_of_month": 5}]
```

Only hhq gets a Kubernetes Service - plugin containers are reachable
exclusively via `localhost:<port>` from within the pod, matching
billtracker-plugin's own "no Ingress, server-to-server only" trust model.

Set `useHhqDbSecret: false` and provide `host`/`port`/`dbname`/`user`/`password`
keys on the plugin's own `existingSecret` if a plugin should use a separate
database instead of sharing hhq's.

For secret material a plugin needs as a *file* rather than an env var (e.g.
billtracker-plugin's `*_PASSWORD_FILE`-style options), add `extraMounts` -
each entry mounts an existing Secret (created outside this chart, never
templated here) into that plugin's container only:

```yaml
plugins:
  - name: billtracker
    ...
    env:
      SIMPLEFIN_PASSWORD_FILE: /secrets/simplefin/password
    extraMounts:
      - name: simplefin
        secretName: billtracker-simplefin
        mountPath: /secrets/simplefin
        items:
          - key: password
            path: password
```

## Extra manifests

`extraManifests` renders arbitrary additional Kubernetes objects alongside
this chart's own resources - useful for things this chart doesn't model
directly (a NetworkPolicy, an ExternalSecret, a second Ingress, etc):

```yaml
extraManifests:
  - apiVersion: v1
    kind: ConfigMap
    metadata:
      name: hhq-extra-example
      namespace: "{{ .Release.Namespace }}"
    data:
      foo: bar
```

Each entry is passed through `tpl`, so quoted string fields can reference
chart template values/functions (`{{ .Release.Namespace }}`,
`{{ include "hhq.fullname" . }}`, etc).

## Ingress

Enable exactly one (or neither, for cluster-internal-only access):

```yaml
ingress:
  enabled: true
  className: nginx
  host: hhq.example.com

# or

httpRoute:
  enabled: true
  parentRefs:
    - name: my-gateway
      namespace: gateway-system
  hostnames: ["hhq.example.com"]
```
