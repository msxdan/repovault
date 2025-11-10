# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.3.0] - 2025-01-10

### Changed
- Removed repository object caching to reduce memory footprint (74% reduction: 397MB → 104MB)
- Set aggressive garbage collection (GOGC=50) for lower memory usage
- Made state saves synchronous to prevent goroutine accumulation
- Optimized git fetch to target specific branches instead of fetching all refs
- Repository size calculation now only runs in verbose log mode
- Log timestamps now include date (format: `2006-01-02 15:04:05`)

### Fixed
- Memory leak from unlimited goroutine spawning during state saves
- Memory retention from cached git repository objects
- Excessive memory usage from redundant ref fetching

## [0.2.0] - Previous Release
