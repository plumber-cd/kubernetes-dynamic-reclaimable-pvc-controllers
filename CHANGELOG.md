# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
