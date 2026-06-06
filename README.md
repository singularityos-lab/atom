# atom

atom is the init and service manager for Singularity OS.

The binary is `sinit`. Invoked as `atomctl` (a symlink to the same binary) it is the control CLI.

`sinit` boots a target from unit files, starts them in parallel by their dependencies, supervises services, handles socket activation, and answers status, log, start/stop and shutdown requests over `atomctl`. It reads `.service`, `.target`, `.socket` and `.timer` units and implements the common directives for ordering, restart, watchdogs and readiness.

## Requirements

- Go 1.26 or newer
- Linux

## Build

```bash
go build ./...
go test ./...
```

`sinit` is a single multicall binary; `atomctl` is a symlink to it:

```bash
go build -o sinit ./cmd/sinit
ln -s sinit atomctl
```

## atom-probe

`cmd/atom-probe` is for development images only. It serves an authenticated shell over TLS and will not start unless a marker (`/etc/atom/probe.enabled` or `/etc/atom/dev.enabled`) and a token are present. Do not ship it in a production image.

## Updates

atom does not manage OS updates. Boot confirmation, rollback and the update flow live in [atomloops](https://github.com/singularityos-lab/atomloops).

## License

GPL-3.0-only. See [LICENSE](LICENSE).
