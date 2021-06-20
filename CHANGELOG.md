# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
