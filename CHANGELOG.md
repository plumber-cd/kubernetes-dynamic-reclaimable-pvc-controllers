# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.1] - 2022-12-11

### Fixed

- Regression in `v0.2.0` caused Releaser to panic on concurrent managed SC read

## [0.2.0] - 2022-12-11

### BREAKING CHANGES

Prior to this change, one of the features of Releaser was to "Automatically associates Releaser with PVs claimed by PVCs that were created by Provisioner with the same `--controller-id`".
From the `README.md` prior to this change:

> ## PV Releaser Controller
> 
> For Releaser to be able to make PVs claimed by Provisioner `Available` after PVC is gone - Releaser and Provisioner must share the same Controller ID.
> 
> ### Associate
> 
> Once `Released` - PVs doesn't have any indication that they were once associated with a PVC that had association with this Controller ID. To establish this relation - we must catch it while PVC still exists and mark it with our label. If Releaser was down the whole time PVC existed - PV could never be associated making it now orphaned and it will stay as `Released` - Releaser can't know it have to make it `Available`.
> 
> Releaser listens for PV creations/updates.
> The following conditions must be met for a PV to be associated with a Releaser:
> 
> - PV doesn't already have `metadata.labels."reclaimable-pv-releaser.kubernetes.io/managed-by"` association.
> - `spec.claimRef` must refer to a PVC that either has `metadata.labels."dynamic-pvc-provisioner.kubernetes.io/managed-by"` or `reclaimable-pv-releaser.kubernetes.io/managed-by` set to this Controller ID. If both labels are set - both should point to this Controller ID.
> - `--disable-automatic-association` must be `false`.
> 
> To establish association Releaser will set itself to `metadata.labels."reclaimable-pv-releaser.kubernetes.io/managed-by"` on this PV.

As disclaimed - that approach was error prone. It was fine most of the time, but if Releaser was down for any noticeable duration of time - it was resulting in PVs piling up in `Released` state, and as the PVC was long gone by then - PVs would remain in that state forever, until manually cleared up.

This mechanism of association through PVC was removed in this release and replaced with a simple Storage Class annotation. In order for Releaser to turn `Released` PV as `Available` - its Storage Class must be annotated with `metadata.annotations."reclaimable-pv-releaser.kubernetes.io/controller-id"` pointing at the `-controller-id` of this Releaser. It can now retro-actively release PVs on startup that it never received events about. As a side effect - `-controller-id` of Provisioner and Releaser doesn't have to match anymore. This unfortunately requires that you use dedicated Storage Class for PVs that must be reclaimable, but that is a fair price to pay if the alternative is unreliable and error prone and might result in expensive storage bills.

You must use Helm charts version `v0.1.0` or above as RBAC is changed in this release to allow read-only access to Storage Classes.

### Changed

- Old PV association via PVC mechanism was removed
- `-disable-automatic-association` option on Releaser was removed
- PVs will only be released now if their Storage Class annotated with `metadata.annotations."reclaimable-pv-releaser.kubernetes.io/controller-id"` pointing at the `-controller-id` of this Releaser

## [0.1.1] - 2022-12-11

### Fixed

- Fixed re-queuing code, previously duplicate items in the queue on updates
- We was not using graceful rate-limit aware code in some places
- Fix leader elect code, previously it would not release the lease after a graceful stop
- Improve basic example with more validation steps
- Start publishing to GitHub Docker Registry `ghcr.io`

## [0.1.0] - 2022-05-14

### Changed

- Updated with Go 1.18 and K8s dependencies 1.24.0.
- Fixed NP panic in releaser when labels were not present on the PV.
- Bumping to `v0.1` - I've been using it for a long while now in prod, and it seems to be working fine.

## [0.1.0-alpha1] - 2022-04-26

### Changed

- Updated with Go 1.17 and K8s dependencies 1.23.6.

## [0.0.4] - 2021-06-20

### Added

- Releaser now checks for both `dynamic-pvc-provisioner.kubernetes.io/managed-by` and `reclaimable-pv-releaser.kubernetes.io/managed-by` labels on the PVC for association making it more independent for use cases where Provisioner is not used at all.

## [0.0.3] - 2021-06-20

### Fixed

- Do not remove PV association if `-disable-automatic-association` was `true` - we didn't set it in the first place

## [0.0.2] - 2021-06-19

### Fixed

- https://hub.docker.com/r/plumbit/kubernetes-dynamic-reclaimable-pvc-controllers/tags had `v` prefix

### Added

- Helm charts

## [0.0.1] - 2021-06-19

### Added

- Initial implementation
