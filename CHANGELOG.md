# Changelog

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
