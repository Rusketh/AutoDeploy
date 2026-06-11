# Command-line reference

AutoDeploy ships three binaries. The **server** is configured through
[environment variables](configuration.md); the **boot client** and **agent** take command-line
flags. All three accept `--version` to print their build version and exit.

---

## `autodeploy-server`

The server takes no operational flags — all configuration comes from the environment (see the
[configuration reference](configuration.md)).

```
autodeploy-server [--version]
```

| Flag | Description |
|------|-------------|
| `--version` | Print the version and exit. |

The installed service version is what the installer uses to fetch matching agent/boot binaries, so
`autodeploy-server --version` is also how you check which release is deployed.

---

## `autodeploy-boot`

The pre-OS imaging client. It runs inside the boot image (initramfs) and is normally launched for
you; the flags are useful for diagnostics and custom boot setups.

```
autodeploy-boot [flags] [command]
```

### Commands

| Command | Description |
|---------|-------------|
| `identify` | Print the machine's SMBIOS identity and exit. Useful for diagnosing hardware matching. |
| `menu` | Fetch the deployment menu from the server and present it interactively. |
| `deploy <image-id>` | Deploy the given image directly, without showing the menu. |

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-server <url>` | *(empty)* | AutoDeploy server base URL, e.g. `https://deploy.example.com`. Required for `menu`/`deploy`. |
| `-disk <device>` | *(auto-detect)* | Target disk device for deployment. Empty auto-detects the internal fixed disk (NVMe preferred, then SATA/SCSI); skips removable and USB-attached disks. Set it — or `autodeploy.disk=` on the kernel cmdline — to force a specific device. A named-but-absent disk fails safe rather than imaging a guessed one. |
| `-work <dir>` | `/run/autodeploy` | Scratch directory. |
| `-site <name>` | *(empty)* | Site name for [payload mirror](../portal/mirrors.md) routing. |
| `-sysfs <path>` | `/sys/class/dmi/id` | DMI sysfs root (override for testing). |
| `-insecure-tls` | `false` | Skip TLS certificate verification (dev only). |
| `-dry-run` | `false` | Log destructive steps without executing them. |
| `-version` | `false` | Print version and exit. |

Examples:

```bash
autodeploy-boot -server https://deploy.example.com menu
autodeploy-boot -server https://deploy.example.com deploy 42
autodeploy-boot identify
```

---

## `autodeploy-agent`

The Windows in-OS and resident agent. It is installed and started automatically during
deployment; the flags and subcommands document how it behaves and support manual use.

```
autodeploy-agent [flags] [subcommand]
```

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `install-service` | Register the agent as a Windows service. |
| `uninstall-service` | Stop and remove the Windows service. |

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-server <url>` | *(empty)* | AutoDeploy server base URL. |
| `-agent-id <uuid>` | *(empty)* | Server-minted agent UUID (resident mode). |
| `-image-id <id>` | `0` | Image ID to deploy now (deployment-time mode), then converge to resident mode. |
| `-uuid <uuid>` | *(empty)* | Machine SMBIOS UUID override (defaults to firmware value). |
| `-check-in <duration>` | `0` | Resident check-in interval, e.g. `5m`. Zero means run once and exit. |
| `-work <dir>` | *(platform default)* | Scratch directory (defaults to `C:\ProgramData\AutoDeploy\work` on Windows). |
| `-no-self-update` | `false` | Skip self-update checks. |
| `-insecure-tls` | `false` | Skip TLS certificate verification (dev only). |
| `-dry-run` | `false` | Log steps without executing them. |
| `-version` | `false` | Print version and exit. |

When run with `-server` and no agent identity (no `-agent-id`, no registry config), the agent
**enrolls itself**: the server registers the machine by SMBIOS identity and returns its minted
agent id, which the agent persists to `HKLM\SOFTWARE\AutoDeploy` — the same provisioning the PXE
flow performs. The machine is fully inventoried (hardware, computer name, AD path) on that first
run, so bringing an existing Windows machine under management is just:

```bash
# 1. Enroll + inventory (elevated prompt; add -image-id <id> to also deploy software)
autodeploy-agent -server https://deploy.example.com

# 2. Make it permanent: auto-start service, resident polling
autodeploy-agent install-service
```

More examples:

```bash
# Resident mode (what the Windows service runs; identity comes from the registry)
autodeploy-agent -check-in 5m

# Deployment-time run: install image 42's software set, then stay resident
autodeploy-agent -server https://deploy.example.com -image-id 42 -check-in 5m

# Service management
autodeploy-agent install-service
autodeploy-agent uninstall-service
```
</content>
