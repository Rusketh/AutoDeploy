# Concepts & glossary

AutoDeploy has a small vocabulary. Once these terms click, the rest of the portal is intuitive.
The diagram below shows how the building blocks combine into a deployable **image**.

```
   ISO ───────────┐
   Unattend ──────┤
   Driver(s) ─────┼──►  IMAGE  ──(bind)──►  MACHINE  ──►  deployment + history
   Loadout(s) ────┤
   Software ──────┘
```

## Payloads

### ISO
An uploaded **Windows installation image**. AutoDeploy extracts it and prepares boot media so the
[boot client](introduction.md#boot-client-autodeploy-boot) can stream it to target machines. See
[Payloads](portal/payloads.md).

### Unattend
An **answer file** for Windows Setup. It captures settings such as locale, time zone, the local
administrator account, OOBE behaviour, and domain join. One unattend can be reused across many
images. See [Payloads → Unattend files](portal/payloads.md#unattend-files).

### Driver package
A bundle of **drivers** with an optional **SMBIOS match** (manufacturer, product, family, SKU,
baseboard, chassis type). At deployment, AutoDeploy applies the driver packages whose match
criteria fit the machine's hardware. See
[Payloads → Driver packages](portal/payloads.md#driver-packages).

## Software

### Software package
A unit of installable software defined by two things: a **detection rule** (how to tell whether
it is already installed) and an **install spec** (how to install it). See
[Software](portal/software.md), and the references on
[detection rules](reference/detection-rules.md) and [install steps](reference/install-steps.md).

### Loadout
A named **group of software packages**, optionally inheriting from a parent loadout. Loadouts let
you assemble reusable software bundles (for example "Standard apps" or "Engineering tools") and
attach them to images. See [Software & loadouts](portal/software.md#loadouts).

## Composition & targeting

### Image
The **deployable recipe**. An image references an ISO, optionally an unattend, and any number of
driver packages, loadouts, and individual software packages. AutoDeploy **resolves** an image
into the concrete set of files and steps that a deployment will apply. See [Images](portal/images.md).

### Machine
A target computer in the **inventory**. Machines are identified by their **SMBIOS UUID** and
appear automatically the first time they network-boot or an agent checks in. A machine record
holds hardware details, deployment history, and its current binding. See [Machines](portal/machines.md).

### Binding
The link between a **machine** and the **image** it should receive. Bind a machine to an image and
it will deploy that image the next time it network-boots (or immediately, via a bulk reimage). See
[Machines → Bindings](portal/machines.md#bindings).

## Scaling & access

### Mirror
A **site-local cache** of payloads. For remote sites or large fleets, a mirror serves ISO and
software data close to the machines so deployments don't all pull from the central server. See
[Mirrors](portal/mirrors.md) and [Scaling](operations/scaling.md).

### Access PIN
An optional **numeric PIN** that gates the PXE boot menu, so only operators who know the PIN can
start a deployment from a booting machine. See [Settings → Access PIN](portal/settings.md#access-pin).

### Account
An **operator login** for the portal and API. The first account (`admin`) is created automatically
on first start. See [Settings → Accounts](portal/settings.md#accounts) and
[Security](operations/security.md).
</content>
