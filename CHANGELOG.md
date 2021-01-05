# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

<!--
Guiding Principles:
* Changelogs are for humans, not machines.
* There should be an entry for every single version.
* The same types of changes should be grouped.
* Versions and sections should be linkable.
* The latest version comes first.
* The release date of each version is displayed.
* Mention whether you follow Semantic Versioning.

Types of changes:
Added - for new features
Changed - for changes in existing functionality
Deprecated - for soon-to-be removed features
Removed - for now removed features
Fixed - for any bug fixes
Security - in case of vulnerabilities
-->

## [2.8.9] - 2020-12-16

- CASMHMS-4241 - Stop collecting/polling River env telemetry if SMA is not available.

## [2.8.8] - 2020-11-13

- CASMHMS-4215 - Added final CA bundle configmap handling to Helm chart.

# [2.8.7] - 2020-10-20

### Security

- CASMHMS-4105 - Updated base Golang Alpine image to resolve libcrypto vulnerability.

# [2.8.6] - 2020-10-16

### Added

- CASMHMS-3763 - Support for TLS certs for Redfish operations.

# [2.8.5] - 2020-09-25

### Added
- CASMHMS-3947 - Support for HPE DL325

# [2.8.4] - 2020-09-10

### Security
- CASMHMS-3993 - Updated hms-hmcollector to use trusted baseOS images.

# [2.8.3] - 2020-09-08

### Fixed
- CASMHMS-3615 - made credentials refresh if the request ever comes back unauthorized.

# [2.8.2] - 2020-07-20

### Changed
- CASMHMS-3783 - Re-enabling by default Telemetry Polling for all River nodes
- Whether polling is enabled can now configured via a Helm chart value override
- Specifying the value `hmcollector_enable_polling=false` as an override in your Loftsman manifest will disable polling in the collector.

# [2.8.1] - 2020-07-17

### Changed
- CASMHMS-3772 - Disabling Telemetry Polling for all River nodes

# [2.8.0] - 2020-06-29

### Added

- CASMHMS-3607 - Added CT smoke test for hmcollector.

# [2.7.6] - 2020-06-05

- CASMHMS-3260 - Now is online installable, upgradable, downgradable.

# [2.7.5] - 2020-05-26

### Changed

- CASMHMS-3433 - bumped resource limits.

# [2.7.4] - 2020-04-27

### Changed

- CASMHMS-2955 - use trusted baseOS images.

# [2.7.3] - 2020-03-30

### Changed

- CASMHMS-2818 - check that subscriptions are still present on controllers.
- CASMHMS-3200 - verify that subscription context remains valid.

# [2.7.2] - 2020-03-13

### Fixed

- Prevented inclusion of empty telemetrey payloads from Gigabyte.

# [2.7.1] - 2020-03-05

### Fixed

- Updated collection of Gigabyte model numbers to include everything known as of now.

# [2.7.0] - 2020-03-04

### Fixed

- Removed Sarama completely in favor of Confluent library built on librdkafka. It was observed that producing large numbers of messages simultaneously would result in only a fraction of them actually making it onto the bus.

# [2.6.1] - 2020-02-07

### Added
- CASMHMS-2642 - Updated liveness/readiness probes.

# [2.6.0] - 2020-01-09

### Added
- Initial version of changelog.