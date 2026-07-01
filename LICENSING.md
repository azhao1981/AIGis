# Licensing

AIGis is an **open-core** project. Different directories carry different licenses.
This file is the authoritative map of the boundary.

## The boundary

| Path | License | Meaning |
|------|---------|---------|
| Everything **except** `ee/` | **GNU AGPLv3** ([`LICENSE`](LICENSE)) | Open source. Free for personal, research, and internal use under the AGPLv3 (source-disclosure obligations apply). |
| `ee/` and everything inside it | **AIGis Enterprise Edition License** ([`ee/LICENSE`](ee/LICENSE)) | Source-available but **proprietary**. Reading and local evaluation only; any production/commercial use needs a commercial license. |
| Binaries built from `cmd/aigis-ee/` | Enterprise | Contain `ee/` code; governed by the Enterprise license. |
| Binaries built from `cmd/aigis/` | AGPLv3 | Open-source build; contain **no** `ee/` code. |

The AGPLv3 in the repository root does **not** apply to files under `ee/`, and the
Enterprise license under `ee/` does **not** apply to anything outside `ee/`.

## Architecture rule that keeps the boundary clean

The dependency direction is strictly one-way:

```
ee/  ──imports──►  internal/  (core)      ✅ allowed
internal/ (core)  ──✕──►  ee/             ❌ never
```

The open-source core exposes extension points (e.g. `server.Middleware` +
`HTTPServer.Use`); the Enterprise Edition implements them and wires them in only
from `cmd/aigis-ee`. As a result:

- `cmd/aigis` (open source) compiles **without** any `ee/` code.
- The core can be built, shipped, and open-sourced independently of `ee/`.

## Two binaries

| Command | Binary | Contents |
|---------|--------|----------|
| `make build` | `bin/aigis` | Open-source gateway (AGPLv3). |
| `make build-ee` | `bin/aigis-ee` | Enterprise gateway (core + `ee/`). |

## Contributing

Contributions to the open-source core follow [`CLA.md`](CLA.md). The `ee/`
directory is maintained by the copyright holder; external contributions there
are handled separately.

## Commercial license

For Enterprise / commercial use, see [`COMMERCIAL.md`](COMMERCIAL.md).
