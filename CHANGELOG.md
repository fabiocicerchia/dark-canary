# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.1.0](https://github.com/fabiocicerchia/dark-canary/compare/v1.0.1...v1.1.0) (2026-08-25)


### Features

* **docs:** build the docs site in Actions and drop Read the Docs ([#25](https://github.com/fabiocicerchia/dark-canary/issues/25)) ([2824709](https://github.com/fabiocicerchia/dark-canary/commit/282470994d8c4c1dd94ef86da8e502e2e1dd0706))

## [1.0.1](https://github.com/fabiocicerchia/dark-canary/compare/v1.0.0...v1.0.1) (2026-08-13)


### Bug Fixes

* security and code-quality findings ([#14](https://github.com/fabiocicerchia/dark-canary/issues/14)) ([925bac9](https://github.com/fabiocicerchia/dark-canary/commit/925bac9a4fbb22f24696f4aba316c100743a3865))

## 1.0.0 (2026-08-06)


### ⚠ BREAKING CHANGES

* **noise:** -rules now parses YAML. Convert existing JSON rulesets; noise.example.json is replaced by noise.example.yaml.

### Features

* **chart:** add a Helm chart ([9184192](https://github.com/fabiocicerchia/dark-canary/commit/918419205db015ec491eee4329a5ff6ad78710b3))
* **dashboard:** serve a stats view at / on the collector port ([e904db4](https://github.com/fabiocicerchia/dark-canary/commit/e904db4bae925cffabcf4f507ec6319a326ab224))
* initial commit ([00dc774](https://github.com/fabiocicerchia/dark-canary/commit/00dc77422cd2ad8fe74301b572daf6bdf677553e))
* **noise:** load rules from YAML instead of JSON ([c38dec5](https://github.com/fabiocicerchia/dark-canary/commit/c38dec55802ea87cbd17cd3c6adb8818ffb352bf))
* **proxy:** route traffic directly, with no nginx or Lua ([99f6701](https://github.com/fabiocicerchia/dark-canary/commit/99f670112ed65aa20e96cf274a3c42ae4c252b8e))


### Bug Fixes

* **ci:** stop security workflows failing on private repos ([#6](https://github.com/fabiocicerchia/dark-canary/issues/6)) ([a5f9d55](https://github.com/fabiocicerchia/dark-canary/commit/a5f9d5553e6140d931b58aede4d7212f2a34b5c3))
* drop the duplicate sources key from Chart.yaml ([8329fc1](https://github.com/fabiocicerchia/dark-canary/commit/8329fc10ec181bffc7081ebc8a959acb73c53c7e))
* **main:** exit non-zero on a failed listener, and stop printing an empty report ([c145cac](https://github.com/fabiocicerchia/dark-canary/commit/c145cace9ae4beb9a2e9da85bfbfcc43045482ed))
* match this repo's actual chart path in the check-yaml exclude ([65f8b0d](https://github.com/fabiocicerchia/dark-canary/commit/65f8b0d05798d19266b27abbcfe92ce151b9057d))
* **pre-commit:** stop check-yaml failing on Helm templates and multi-doc manifests ([f3255f6](https://github.com/fabiocicerchia/dark-canary/commit/f3255f629c39abf23ec841922cb661a12312250f))
* **report:** return write failures instead of always returning nil ([d7fb4e5](https://github.com/fabiocicerchia/dark-canary/commit/d7fb4e50a6ec48c4d1a2586d00ad677eaa384223))
* satisfy errcheck on the writes that report their errors out of band ([08e49e3](https://github.com/fabiocicerchia/dark-canary/commit/08e49e39ea050baa7df021eb11399a53360d3343))
* spell "unparsable" the way the typos linter expects ([94c3948](https://github.com/fabiocicerchia/dark-canary/commit/94c3948150fc795279311fa06717662cbd3971d4))

## [Unreleased]

### Added
### Changed
### Deprecated
### Removed
### Fixed
### Security

## [0.1.0] - 2026-08-01

### Added
- Initial release.

[Unreleased]: https://github.com/fabiocicerchia/dark-canary/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/fabiocicerchia/dark-canary/releases/tag/v0.1.0
