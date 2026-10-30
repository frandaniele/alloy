---
canonical: https://grafana.com/docs/alloy/latest/set-up/otel_engine/custom-builds/
description: Learn how to build a customized Grafana Alloy binary with only the components you need
menuTitle: Custom builds
review_date: 2026-10-06
title: Build a custom Grafana Alloy binary
weight: 500
---

# Build a custom {{% param "FULL_PRODUCT_NAME" %}} binary

You can build a {{< param "PRODUCT_NAME" >}} binary that includes only the components you need, or that adds components the official release doesn't bundle.
For example, you can build a {{< param "PRODUCT_NAME" >}} binary that only collects logs.

There are two ways to build a custom binary:

| If you want to                                                                                            | Use                                                                           | What you can select                                                                                     |
|-----------------------------------------------------------------------------------------------------------|-------------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------|
| Run the {{< param "DEFAULT_ENGINE" >}} inside your own OpenTelemetry Collector distribution               | [Your own distribution with the `alloyengine` extension][EmbedExtension]      | The Collector components in your own builder manifest                                                   |
| Add or remove the OpenTelemetry Collector components that the {{< param "OTEL_ENGINE" >}} uses            | [Build {{< param "PRODUCT_NAME" >}} from its repository][BuildFromRepository] | The Collector components in `builder-config.yaml`                                                       |
| Include only some {{< param "DEFAULT_ENGINE" >}} components, or remove configuration conversion           | [Build {{< param "PRODUCT_NAME" >}} from its repository][BuildFromRepository] | The {{< param "DEFAULT_ENGINE" >}} components and configuration conversion in `alloy-components.yaml` |

{{< admonition type="caution" >}}
Grafana doesn't offer commercial support for custom builds.
{{< /admonition >}}

{{< docs/shared lookup="stability/public_preview_otel.md" source="alloy" version="<ALLOY_VERSION>" >}}

## Embed the {{% param "DEFAULT_ENGINE" %}} in your own Collector distribution

If you build your own OpenTelemetry Collector distribution with the [OpenTelemetry Collector Builder (OCB)][OCB], you can run the {{< param "DEFAULT_ENGINE" >}} inside it with the `alloyengine` extension.
Refer to the [`alloyengine` extension README](https://github.com/grafana/alloy/blob/main/extension/alloyengine/README.md#include-alloyengine-extension-in-an-ocb-distribution) for instructions.

The extension always includes every {{< param "DEFAULT_ENGINE" >}} component.
You select the Collector components in your own builder manifest, but you can't select {{< param "DEFAULT_ENGINE" >}} components, because OCB doesn't read `alloy-components.yaml`.
To select {{< param "DEFAULT_ENGINE" >}} components, [build {{< param "PRODUCT_NAME" >}} from its repository][BuildFromRepository] as described in the next section.

## Build {{% param "PRODUCT_NAME" %}} from its repository

Grafana builds {{< param "PRODUCT_NAME" >}} with `make alloy` from two files in the `collector` directory of the {{< param "PRODUCT_NAME" >}} repository:

- **[`builder-config.yaml`][BuilderConfig]:** An OCB manifest.
  Its `receivers`, `processors`, `exporters`, `connectors`, and `extensions` sections select the Collector components that the {{< param "OTEL_ENGINE" >}} uses.
- **[`alloy-components.yaml`][AlloyComponents]:** Selects the {{< param "DEFAULT_ENGINE" >}} components, and whether the build can convert configuration files from other formats.

`make alloy` runs OCB on `builder-config.yaml`.
It then runs the {{< param "PRODUCT_NAME" >}} generator in `collector/generator`, which reads `alloy-components.yaml` and generates the matching component imports.
OCB doesn't read `alloy-components.yaml`, so you need the `make alloy` build in the {{< param "PRODUCT_NAME" >}} repository to select {{< param "DEFAULT_ENGINE" >}} components.

### Before you begin

Make sure you have the following tools:

- Git
- Go {{< param "GO_VERSION" >}} or later
- Node.js
- Docker, if you want to build the container image

The build compiles C dependencies, so you also need the following for your platform:

- On Linux, the systemd headers.
  On Debian-based distributions, run `sudo apt-get install libsystemd-dev`.
- On Windows, a 64-bit [mingw-w64][MinGW] gcc toolchain.
  Grafana recommends the [WinLibs][WinLibs] build.

### Clone the repository

Clone the {{< param "PRODUCT_NAME" >}} repository and change to the repository root.
The following steps assume you run commands from this directory.

```shell
git clone https://github.com/grafana/alloy.git
cd alloy
```

To build from a specific release, fetch tags and check out the tag after you clone:

```shell
git fetch --tags
git checkout <RELEASE_TAG>
```

Replace _`<RELEASE_TAG>`_ with the [release tag][ReleaseTag] you want.

### Select OpenTelemetry Collector components

The OCB sections of `collector/builder-config.yaml` list the Collector components that the {{< param "OTEL_ENGINE" >}} can use.

- **Remove a component:** Delete its `- gomod: ...` line from the appropriate section.
- **Add a component:** Append a `- gomod:` line that points at the module path and version you want.
  Follow the same pattern as the other entries.

### Select {{% param "DEFAULT_ENGINE" %}} components

The `components` field of `collector/alloy-components.yaml` lists the {{< param "DEFAULT_ENGINE" >}} components to include in the build.
A `*` in a name matches any characters:

- **`"*"`:** Includes every component.
  This is the default.
- **`prometheus.*`:** Includes every component whose name starts with `prometheus.`, for example `prometheus.scrape` and `prometheus.exporter.unix`.
- **`loki.write`:** Includes only `loki.write`.

Quote a name that starts with `*`, such as `"*"`, because YAML reads an unquoted `*` as the start of an alias.

The following example includes only the components needed to read log files and send them to Loki:

```yaml
components: [local.file_match, loki.source.file, loki.process, loki.write]
```

Use the component names listed in the [components reference](../../../reference/components/).
The build includes the whole package that registers each component you list, and one package can register several components.
For example, listing `discovery.ec2` also includes `discovery.aws` and `discovery.lightsail`.
The list selects packages, so it's not always an exact allowlist of components.
If a name or pattern in the list matches no component, the build fails and lists the components in the same namespace, for example all `loki.*` components.

If you omit the `components` field, the build includes every component.
An empty list, `components: []`, includes no {{< param "DEFAULT_ENGINE" >}} components.

The `otelcol.*` components of the {{< param "DEFAULT_ENGINE" >}} are separate from the Collector components in `builder-config.yaml`.
For example, the `otlpreceiver` entry in the `receivers` section doesn't make `otelcol.receiver.otlp` available to the {{< param "DEFAULT_ENGINE" >}}.
List `otelcol.receiver.otlp` in the `components` field to include it.

{{< param "PRODUCT_NAME" >}} rejects configurations that use components which aren't included in the build.
For example, `alloy run` and `alloy validate` report that they can't find the definition of the component.

### Remove configuration conversion

The `converters` field of `collector/alloy-components.yaml` controls whether the build can convert configuration files from other formats, such as Prometheus, Promtail, and OpenTelemetry Collector configuration files.
It defaults to `true`.

The converters depend on most {{< param "DEFAULT_ENGINE" >}} components.
If you include only some components, set `converters` to `false` to reduce the binary size:

```yaml
components: [local.file_match, loki.source.file, loki.process, loki.write]
converters: false
```

A build without converters returns an error when you run [`alloy convert`](../../../reference/cli/convert/) or start {{< param "PRODUCT_NAME" >}} with a [`--config.format`](../../../reference/cli/run/) other than `alloy`.

### Build the binary

After you edit `collector/builder-config.yaml` and `collector/alloy-components.yaml`, build {{< param "PRODUCT_NAME" >}}:

```shell
make alloy
```

This command regenerates the Collector distribution and the {{< param "DEFAULT_ENGINE" >}} component imports from both files before it builds, so your edits take effect.
The binary in `build/` behaves like a standard `alloy` build.
To skip the UI build when the UI assets already exist, run `SKIP_UI_BUILD=1 make alloy`.
To strip debug information from the binary like the official releases do, set `RELEASE_BUILD=1`.

To confirm that your Collector components are in the build, run the following command:

```shell
./build/alloy otel components
```

The command lists each bundled component with its module path, version, and stability level.
Use [`alloy otel`][OTelCommand] to run collector YAML against your custom bundle.

### Build a Docker image

To build a Docker image from your custom build files, run the following command:

```shell
make alloy-image ALLOY_IMAGE=<REGISTRY>/<IMAGE_NAME>
```

Replace the following:

- _`<REGISTRY>`_: Your container registry.
- _`<IMAGE_NAME>`_: Your image name and tag, for example `alloy-logs:v1`.

If you don't set `ALLOY_IMAGE`, the build names the image `grafana/alloy:latest`.

### Binary sizes

The following table shows approximate sizes of custom builds, so you can estimate how much you save by including fewer components.
The sizes depend on the {{< param "PRODUCT_NAME" >}} version, the platform, and the dependencies of each component, so treat them as ballpark figures.

<!-- TODO: Fill in the binary sizes that `make test-custom-builds` logs on linux/amd64, and state the Alloy version they were measured on. -->

| Build | `alloy-components.yaml` | Size |
|-------|-------------------------|------|
| TBD   | TBD                     | TBD  |

## Next steps

- [Run the {{< param "OTEL_ENGINE" >}}](../) with your custom build.
- [Components reference](../../../reference/components/) for the names of {{< param "DEFAULT_ENGINE" >}} components.
- [OpenTelemetry Collector Builder documentation](https://opentelemetry.io/docs/collector/custom-collector/) for the OCB manifest format.

[AlloyComponents]: https://github.com/grafana/alloy/blob/main/collector/alloy-components.yaml
[BuilderConfig]: https://github.com/grafana/alloy/blob/main/collector/builder-config.yaml
[BuildFromRepository]: #build-alloy-from-its-repository
[EmbedExtension]: #embed-the-default-engine-in-your-own-collector-distribution
[MinGW]: https://www.mingw-w64.org/
[OCB]: https://opentelemetry.io/docs/collector/custom-collector/
[OTelCommand]: ../../../reference/cli/otel/
[ReleaseTag]: https://github.com/grafana/alloy/releases
[WinLibs]: https://winlibs.com/
