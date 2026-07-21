# Changelog

## [0.10.1](https://github.com/yorah/dockbrr/compare/v0.10.0...v0.10.1) (2026-07-21)


### Bug Fixes

* **changelog:** refresh the current-version baseline when the image moves ([#62](https://github.com/yorah/dockbrr/issues/62)) ([4549058](https://github.com/yorah/dockbrr/commit/4549058b628bad71e23ee1f2f123887b43458e41))
* **self-update:** surface job outcome and reload after apply ([#61](https://github.com/yorah/dockbrr/issues/61)) ([8df304e](https://github.com/yorah/dockbrr/commit/8df304eb880dec736e8ef2537cd51ab96095a0f4))

## [0.10.0](https://github.com/yorah/dockbrr/compare/v0.9.1...v0.10.0) (2026-07-21)


### Features

* **ui:** explain the project health dot with a tooltip ([#59](https://github.com/yorah/dockbrr/issues/59)) ([5b75cfa](https://github.com/yorah/dockbrr/commit/5b75cfa93ca7336b9bd487cf4e74d9351cfde41d))

## [0.9.1](https://github.com/yorah/dockbrr/compare/v0.9.0...v0.9.1) (2026-07-21)


### Bug Fixes

* **compose:** collapse per-layer pull terminal states in job log ([#57](https://github.com/yorah/dockbrr/issues/57)) ([e503c96](https://github.com/yorah/dockbrr/commit/e503c96a599e418bf6543d81db9f5b629a5a10c6))

## [0.9.0](https://github.com/yorah/dockbrr/compare/v0.8.0...v0.9.0) (2026-07-21)


### Features

* scan progress + cross-navigation button disabling ([#54](https://github.com/yorah/dockbrr/issues/54)) ([08925e2](https://github.com/yorah/dockbrr/commit/08925e299479613e97f5b20b939b63488d52601a))


### Bug Fixes

* self-update detection under Docker Compose + route apply-on-self to the self-updater ([#55](https://github.com/yorah/dockbrr/issues/55)) ([74f1479](https://github.com/yorah/dockbrr/commit/74f1479379d55e100d0bed768cf22ea149ecb2c4))

## [0.8.0](https://github.com/yorah/dockbrr/compare/v0.7.0...v0.8.0) (2026-07-20)


### Features

* check for new dockbrr version on container startup ([#52](https://github.com/yorah/dockbrr/issues/52)) ([0d930de](https://github.com/yorah/dockbrr/commit/0d930def94dcb5f8ad732d8b8f4c1f611846f04a))

## [0.7.0](https://github.com/yorah/dockbrr/compare/v0.6.1...v0.7.0) (2026-07-20)


### Features

* manual check-for-updates in Application settings ([#49](https://github.com/yorah/dockbrr/issues/49)) ([f0d18ad](https://github.com/yorah/dockbrr/commit/f0d18ad7e66de9cadfd74749c20b74697834ccc2))


### Bug Fixes

* **build:** stamp commit/dirty via ldflags, not Go vcs info ([#51](https://github.com/yorah/dockbrr/issues/51)) ([d188b25](https://github.com/yorah/dockbrr/commit/d188b251eead92aa9da60332dbab3e5efc4f6a5c))
* **changelog:** reach raw CHANGELOG.md fallback when releases API errors ([#48](https://github.com/yorah/dockbrr/issues/48)) ([253c726](https://github.com/yorah/dockbrr/commit/253c72612d8c2c5d4cfdaae6a8e21fff4c0467e9))

## [0.6.1](https://github.com/yorah/dockbrr/compare/v0.6.0...v0.6.1) (2026-07-20)


### Bug Fixes

* **detect:** resolve floating-tag version by config digest ([#46](https://github.com/yorah/dockbrr/issues/46)) ([bce28d2](https://github.com/yorah/dockbrr/commit/bce28d298ccbc425c582b878eb1d72678d7dcbde))

## [0.6.0](https://github.com/yorah/dockbrr/compare/v0.5.0...v0.6.0) (2026-07-19)


### Features

* **self-update:** update dockbrr's own container from the UI. A detached helper runs dockbrr's image with a `self-update-swap` subcommand; the pull happens in-process (dockbrr stays up if it fails) and the swap rolls back to the old image on failure ([#42](https://github.com/yorah/dockbrr/issues/42))
* **local images:** compose `build:` services are classified `local`, skip registry probes, and show a grey "Local" badge excluded from the update tallies ([#42](https://github.com/yorah/dockbrr/issues/42))
* refuse mutating jobs that target dockbrr's own container ([#42](https://github.com/yorah/dockbrr/issues/42))
* show the target project/service in the Jobs list and add a rollback action for the latest finished apply ([#42](https://github.com/yorah/dockbrr/issues/42))
* manual check-all bypasses the detect cache and re-surfaces rolled-back updates ([#42](https://github.com/yorah/dockbrr/issues/42))
* `not_found` check status with tooltips; persist the dashboard collapse state ([#42](https://github.com/yorah/dockbrr/issues/42))
* actionable hint when compose files are unreachable; live countdowns on auto-dismissing toasts and the apply panel ([#42](https://github.com/yorah/dockbrr/issues/42))


### Bug Fixes

* **compose:** preserve file ownership on write-back; fix the pull-progress filter for compose's indented output ([#42](https://github.com/yorah/dockbrr/issues/42))
* **detect:** cache-hit path no longer closes semver updates it can't judge ([#42](https://github.com/yorah/dockbrr/issues/42))
* **web:** keep job-backed action buttons disabled until the job finishes; clear busy state after refetch; auto-close the apply panel on success ([#42](https://github.com/yorah/dockbrr/issues/42))
* **web:** hide all-gone projects from the sidebar; settings and shell scroll polish ([#42](https://github.com/yorah/dockbrr/issues/42))
* **web:** job panel title and success label follow the job type ([#42](https://github.com/yorah/dockbrr/issues/42))
* stamp live-inspected container state after lifecycle jobs ([#42](https://github.com/yorah/dockbrr/issues/42))

## [0.5.0](https://github.com/yorah/dockbrr/compare/v0.4.2...v0.5.0) (2026-07-17)


### Features

* current-version changelog for up-to-date services ([#39](https://github.com/yorah/dockbrr/issues/39)) ([46edee3](https://github.com/yorah/dockbrr/commit/46edee32902f8362e9a8363976a193ca54c32098))
* self-update notification in sidebar ([#40](https://github.com/yorah/dockbrr/issues/40)) ([ef708c9](https://github.com/yorah/dockbrr/commit/ef708c9eb43c5ba7757effc4a64e1972d2f87e4b))
* **web:** collapse dashboard projects by default ([#38](https://github.com/yorah/dockbrr/issues/38)) ([0d100e0](https://github.com/yorah/dockbrr/commit/0d100e0dc0749f29bb69731ab9e407ec05e33c68))


### Bug Fixes

* **changelog:** keep release notes for build-suffixed tags ([#37](https://github.com/yorah/dockbrr/issues/37)) ([f6aa0da](https://github.com/yorah/dockbrr/commit/f6aa0da115c43b54ac767c85262f68e290b5309c))
* **detect:** supersede stale open update when service is up to date ([#35](https://github.com/yorah/dockbrr/issues/35)) ([032e8bf](https://github.com/yorah/dockbrr/commit/032e8bf7ad641f3657ef3bf5a0890abca73b098a))

## [0.4.2](https://github.com/yorah/dockbrr/compare/v0.4.1...v0.4.2) (2026-07-17)


### Bug Fixes

* **detect:** show current version for up-to-date floating tags ([#34](https://github.com/yorah/dockbrr/issues/34)) ([60b3a35](https://github.com/yorah/dockbrr/commit/60b3a35f38773b5817231fdb66a71246e48fe53d))
* **web:** mute the sha256 digest, keep the version at full weight ([#32](https://github.com/yorah/dockbrr/issues/32)) ([478b7c1](https://github.com/yorah/dockbrr/commit/478b7c145825b6669280299ef6093dcea23d2df7))

## [0.4.1](https://github.com/yorah/dockbrr/compare/v0.4.0...v0.4.1) (2026-07-17)


### Bug Fixes

* **detect:** scope semver update scan to the same tag stream ([#30](https://github.com/yorah/dockbrr/issues/30)) ([98eaa96](https://github.com/yorah/dockbrr/commit/98eaa96108f0af0bf619560a4e2b23c62c4bccd9))
* **web:** grey the reverse-resolved Current-image version again ([#29](https://github.com/yorah/dockbrr/issues/29)) ([09c8b9c](https://github.com/yorah/dockbrr/commit/09c8b9c48fa67e4acc39d9f3d2e6cf1f33ea38b5))

## [0.4.0](https://github.com/yorah/dockbrr/compare/v0.3.1...v0.4.0) (2026-07-17)


### Features

* signal GitHub rate-limit when a changelog can't be fetched ([#27](https://github.com/yorah/dockbrr/issues/27)) ([7d591e1](https://github.com/yorah/dockbrr/commit/7d591e1e1f79842b5edfc3dfb26bb32ed2b8f2f8))
* **web:** show project health indicator on the dashboard project row ([#25](https://github.com/yorah/dockbrr/issues/25)) ([23c3054](https://github.com/yorah/dockbrr/commit/23c30542f3478432479f396cc47206ab454f1533))


### Bug Fixes

* **detect:** name the "from" version of a floating tag from the running image label ([#26](https://github.com/yorah/dockbrr/issues/26)) ([342900a](https://github.com/yorah/dockbrr/commit/342900a82d26ead135a56a7e47c6d6c2a67ec453))
* **web:** match Current-image version weight to Latest column ([#24](https://github.com/yorah/dockbrr/issues/24)) ([5c82082](https://github.com/yorah/dockbrr/commit/5c820829d4bc6e031c8fe5dd6fb8e941b8b4705d))

## [0.3.1](https://github.com/yorah/dockbrr/compare/v0.3.0...v0.3.1) (2026-07-16)


### Bug Fixes

* **web:** dashboard UX fixes (remove debounce, compose gating, sticky sidebar, resolved versions) ([#22](https://github.com/yorah/dockbrr/issues/22)) ([404e018](https://github.com/yorah/dockbrr/commit/404e018245449f0440d8bf316ccf22c7ba0524e4))

## [0.3.0](https://github.com/yorah/dockbrr/compare/v0.2.0...v0.3.0) (2026-07-16)


### Features

* **web:** container remove UX — per-row button + Loose bulk action ([#21](https://github.com/yorah/dockbrr/issues/21)) ([31a0948](https://github.com/yorah/dockbrr/commit/31a094801d80856f27308b4496d0b6552c8f5a05))


### Bug Fixes

* **store:** preserve resolved versions on digest-only re-detect ([#19](https://github.com/yorah/dockbrr/issues/19)) ([b360ff7](https://github.com/yorah/dockbrr/commit/b360ff720e8738b79287aa70c2eb6049e68b8250))

## [0.2.0](https://github.com/yorah/dockbrr/compare/v0.1.0...v0.2.0) (2026-07-16)


### Features

* **detect:** name floating-tag versions via reverse digest lookup ([#17](https://github.com/yorah/dockbrr/issues/17)) ([70cc77e](https://github.com/yorah/dockbrr/commit/70cc77eff648b1857920f470408948525b74fb40))
* loose container grouping + workload lifecycle (start/stop/restart, remove, logs, standalone apply) ([#18](https://github.com/yorah/dockbrr/issues/18)) ([f0ddac9](https://github.com/yorah/dockbrr/commit/f0ddac91e727c615dad70f3fc1d353fbcaaa1217))
* **web:** add GitHub repo link to sidebar footer ([#16](https://github.com/yorah/dockbrr/issues/16)) ([b938940](https://github.com/yorah/dockbrr/commit/b938940c31f9f0e99883225d8c6a1969a789aea6))


### Bug Fixes

* **web:** drop removed services from Pinned dashboard count ([#14](https://github.com/yorah/dockbrr/issues/14)) ([7b02c8e](https://github.com/yorah/dockbrr/commit/7b02c8e39610f1725580b2aab6f01d481fc0098c))

## 0.1.0 (2026-07-14)


### Features

* initial public release of dockbrr ([088a74f](https://github.com/yorah/dockbrr/commit/088a74f52bf670386192640be2081e3878da5016))


### Miscellaneous Chores

* force initial release to 0.1.0 ([#11](https://github.com/yorah/dockbrr/issues/11)) ([b939a9a](https://github.com/yorah/dockbrr/commit/b939a9ab6856ada953361d2d8900bffe7dd4483d))
