# Changelog

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions follow [Semantic Versioning](https://semver.org/); until `1.0.0`, a minor bump can still break a flag or a config key.

## Unreleased

## 0.1.4 - 2026-09-30

### Changed
- A stable release updates the Homebrew formula as soon as it is published, instead of on the tap's next six-hourly run.

### Fixed
- `examples/config.yaml` named `~/.config/accel/config.d` and `accel --diagnose` from before the rename; it now names the `siltide` paths the loader reads.

## 0.1.3 - 2026-09-28

### Added
- AMD and Intel rows show the current and maximum PCIe link generation and width from sysfs, so health can catch a card running at reduced width or generation.
- A device that reports memory but no utilization, temperature or power gets an informational health note instead of a silent blank row.

### Changed
- AMD is confirmed on real hardware (Radeon RX 9060 XT, RDNA4). On that card amdgpu returns EBUSY for its SMU sensors, so utilization, temperature and power read as N/A.
- Table columns, the tab bar and the footer are separated by a rule in the theme's border colour.

## 0.1.2 - 2026-09-26

### Changed
- The GitHub owner is now `moezdil`; every link, install command and the Homebrew tap point there.

## 0.1.1 - 2026-09-19

### Fixed
- The published binaries carry a build-provenance attestation. In 0.1.0 they did not: the release workflow looked for them at `dist/siltide-*`, and goreleaser keeps each build at `dist/siltide_<os>_<arch>/siltide` and gives it the published name on upload, so the glob matched nothing and the step attested the packages alone without saying it had skipped anything. `gh attestation verify` on a 0.1.0 binary answers 404; on 0.1.1 it answers. A count before the attestation, in the release workflow and on every pull request, stops an empty glob passing quietly again.
- A missing `AUR_SSH_KEY` is a warning rather than a failed release.

## 0.1.0 - 2026-09-19

First tagged release. Every push to `main` had been publishing a pre-release; this is the version the install script, `siltide --update` and Homebrew resolve to by default.

### Added

**Hardware.** 15 accelerator vendors: NVIDIA through NVML loaded with `dlopen` and no cgo, with Xid events, NVLink, ECC, row remaps and PCIe AER; Apple silicon through ioreg, IOReport and SMC, including per-process GPU time; AMD and Intel through sysfs and DRM `fdinfo`; and Huawei Ascend, AWS Neuron, Biren, Cambricon, Enflame, Hygon, Iluvatar CoreX, Kunlunxin, MetaX, Moore Threads and VastAI through their own tools, one captured fixture behind every parser. NVIDIA DCGM profiling metrics when `dcgmi` is present.

**The terminal.** 16 tabs on the number and letter keys, each hiding itself when it has nothing to show. A filter language with free words, `key:value` pairs, comparisons and negation. A command bar that lists what it accepts as you type. Marking with `x` and `X`, and `y` and `Y` to copy the identifiers or the command for the marked rows, which siltide never runs itself. 16 themes including `paper` for a light terminal, plus user-defined ones. Mouse support, rebindable keys, bookmarks, and a view that reopens where you left it.

**History.** An on-disk time machine with scrubbing, zoom, per-metric provenance and per-process trends. `--record` and `--replay` hand an incident to someone else.

**Kubernetes and Slurm.** Processes trace back through their container to the pod and the workload, from `/var/log/pods`, the kubelet pod-resources socket or `scontrol`. Node capacity, allocatable and requested accelerator resources, MIG names included. `describe` and container logs from inside the interface.

**Fleets and integration.** `--listen` serves `/api/snapshot`, `/api/summary`, `/api/events`, `/api/history` and Prometheus `/metrics`; `--remote` attaches a terminal to another siltide; an SSH provider runs vendor tools on hosts without installing siltide there. `--mcp-stdio` and `--mcp-http` answer an agent over the Model Context Protocol with seven read-only tools. A plugin is a signed JSON manifest that adds a view and runs no code.

**Operations.** Cost and waste per workload, energy and carbon tracking, alert outputs to a webhook, Slack or Alertmanager, anomaly detection, topology-aware placement hints, and `--diagnose` for what siltide saw and did not.

**Getting it and keeping it.** An install script that verifies the download against the published checksums, `siltide --update` that does the same and replaces the binary with a rename, Linux and macOS binaries, deb, rpm, apk and Arch packages, a container image, a Nix flake, a Homebrew tap and an AUR package. Every artifact carries an SBOM and a build-provenance attestation.

### Security

- `--listen` refuses a non-loopback address without TLS and a token.
- The Model Context Protocol endpoint is loopback only, and checks the `Host` header so a page in a browser cannot be pointed at it.
- Nothing leaves the machine that was not configured to leave it.
