# Contributing

```bash
git clone https://github.com/singularityos-lab/atom
cd atom
go build ./...
go test ./...
```

`sinit` runs as PID 1, so a regression is a failed boot. Keep changes small and
tested, and match the surrounding style. By submitting a contribution you agree
to the [CLA](CLA.md).

Licensed GPL-3.0-only.
