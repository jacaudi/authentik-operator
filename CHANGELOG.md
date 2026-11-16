# Changelog

## 0.1.0 (2026-11-16)


### ⚠ BREAKING CHANGES

* **api:** adoption and deletion policy for OAuth providers and applications ([#18](https://github.com/jacaudi/authentik-operator/issues/18))

### Features

* **api:** adoption and deletion policy for OAuth providers and applications ([#18](https://github.com/jacaudi/authentik-operator/issues/18)) ([7fda11f](https://github.com/jacaudi/authentik-operator/commit/7fda11fcdbdc83da3eefc5cf2eb65a62835fa784))
* **api:** make AuthentikDeployment adoption explicit and opt-in ([12c4646](https://github.com/jacaudi/authentik-operator/commit/12c4646a332abd72fb5ed9ebb1b08dd412d83a5b))
* **api:** make AuthentikDeployment spec.mode mutable ([3f095b6](https://github.com/jacaudi/authentik-operator/commit/3f095b665748d7f84d4a6845c080957a8f64469d))
* Authentik API client, OAuth provider, application and credentials Secret, plus CI ([#1](https://github.com/jacaudi/authentik-operator/issues/1)) ([5a32db1](https://github.com/jacaudi/authentik-operator/commit/5a32db1e6cc4edf6880e754a024fa7c057be3a1c))
* **migrate:** JeffResc converter and zero-downtime migration runbook (TASK-09) ([#26](https://github.com/jacaudi/authentik-operator/issues/26)) ([4203915](https://github.com/jacaudi/authentik-operator/commit/42039153b4a89dcc84cf222ac6c75fca80d603e3))
* **scaffold:** initialize kubebuilder project with five API kinds ([1289155](https://github.com/jacaudi/authentik-operator/commit/12891554e475f4dd0d53c5f90758bcee3ff0c76c))


### Bug Fixes

* **authentik:** retry PATCH on 5xx and transport errors ([2d8f060](https://github.com/jacaudi/authentik-operator/commit/2d8f060d558743948d7f920d927cae0975b0a45b))
* **ci:** ignore bin/ in yamllint ([d6ac6d5](https://github.com/jacaudi/authentik-operator/commit/d6ac6d5b399befd5cbe8671dadb5f37d896092d3))
* **controller:** keep provider ownership after a clientId change ([965159f](https://github.com/jacaudi/authentik-operator/commit/965159f6d6aea4ef0f297a2f03f907d87bfdee00))
* **controller:** recover a lost provider create with a pinned clientId ([5dfdd37](https://github.com/jacaudi/authentik-operator/commit/5dfdd37f40c6c809fb8a648ed2208e5e192a7048))
* correctness follow-ups to [#1](https://github.com/jacaudi/authentik-operator/issues/1) (ownership, retries, mutable mode, one condition-error type) ([59bb1ee](https://github.com/jacaudi/authentik-operator/commit/59bb1ee11d1d574d3d915a60aa3371917ded96da))
* **credentials:** refuse YAML line breaks NEL, U+2028 and U+2029 in variables ([23b81c6](https://github.com/jacaudi/authentik-operator/commit/23b81c661b7f292c063e086c1cb35336eddfe111))
* **deps:** clear main's security and lint failures ([1fdb514](https://github.com/jacaudi/authentik-operator/commit/1fdb514554796e5b585ce3f2765f1d23eef57f59))
* **lint:** clear the findings of the new baseline ([d8035e6](https://github.com/jacaudi/authentik-operator/commit/d8035e6d4aba63fc9cebd5a4d4f0eee393763179))
* **rbac:** drop unused patch verb on Secrets ([48ce53c](https://github.com/jacaudi/authentik-operator/commit/48ce53c3379e34d7d85fd679937473562c723ebe))


### Documentation

* **agents:** record that the Go toolchain pin is bumped by hand ([714f6a1](https://github.com/jacaudi/authentik-operator/commit/714f6a167fd4a4d48278e6bfefb2561cda888b5e))


### Dependencies and Maintenance

* Configure Renovate ([ecca511](https://github.com/jacaudi/authentik-operator/commit/ecca5113fb5caf4e3808f5d0af1e979aa3c81418))
* **deps:** update amannn/action-semantic-pull-request action to v6.1.2 ([69ad530](https://github.com/jacaudi/authentik-operator/commit/69ad5303f747799d00e2b685bf1ea242133ef99a))
* **deps:** update astral-sh/setup-uv action to v10 ([a00b494](https://github.com/jacaudi/authentik-operator/commit/a00b494d941b0dcccbb73522e6fbb368d8eb2dc7))
* **deps:** update dependency go to v1.27.4 ([2f11618](https://github.com/jacaudi/authentik-operator/commit/2f11618b4981a9d9a2ab8a76fe9fd5a475bd54e7))
* **deps:** update docker/login-action action to v4.8.0 ([356303c](https://github.com/jacaudi/authentik-operator/commit/356303cf63f822f2d7e0e6d1d221fbc2af7d9eba))
* **deps:** update ghcr.io/devcontainers/features/docker-in-docker docker tag to v4 ([890efab](https://github.com/jacaudi/authentik-operator/commit/890efab54b2687b39d88edcdc5c8234153c3daea))
* **deps:** update github/codeql-action action to v4.39.3 ([63a3bd0](https://github.com/jacaudi/authentik-operator/commit/63a3bd032cbb8a57e520bb019316a7e51af5ef74))
* **deps:** update golang docker tag to v1.27 ([72083df](https://github.com/jacaudi/authentik-operator/commit/72083dfd107a84f57b5a02091aca13141845f83a))
* **deps:** update golang:1.26 examples in comments to 1.27 ([ea6e69f](https://github.com/jacaudi/authentik-operator/commit/ea6e69fd13832157cbb21d36f94844f32fb11f23))
* **deps:** update module github.com/onsi/ginkgo/v2 to v2.34.0 ([c09b9a5](https://github.com/jacaudi/authentik-operator/commit/c09b9a588952f4c63d7e75456ec4f3234758e164))
* **deps:** update module github.com/onsi/gomega to v1.44.0 ([2c0b6b0](https://github.com/jacaudi/authentik-operator/commit/2c0b6b0d55f3d9b9ac47367fca352040612f87cd))
* **deps:** update module go.yaml.in/yaml/v3 to v3.0.6 ([e288995](https://github.com/jacaudi/authentik-operator/commit/e288995bdcc5e3401e0f78fa2ac1eaca2a930c0e))
* **deps:** update module go.yaml.in/yaml/v3 to v4 ([e077c4f](https://github.com/jacaudi/authentik-operator/commit/e077c4f154fcbede21aae2847d5fdb515bf71922))
* **deps:** update module goauthentik.io/api/v3 to v3.2026080.5 ([692cc57](https://github.com/jacaudi/authentik-operator/commit/692cc57149765fe1405e1cf06e5cad64df9da7ed))
* **deps:** update module golang.org/x/time to v0.16.0 ([720d469](https://github.com/jacaudi/authentik-operator/commit/720d4699dd2173ca527b867a0f0f3d2c22142ce9))
* **deps:** update module k8s.io/client-go to v0.37.2 ([c1d59ff](https://github.com/jacaudi/authentik-operator/commit/c1d59ff07778173c568b247632f3d73c09eba823))
* **deps:** update module sigs.k8s.io/controller-runtime to v0.25.2 ([e918067](https://github.com/jacaudi/authentik-operator/commit/e9180676739a413078880ea8f35952c38c166d0e))
* **deps:** update zizmorcore/zizmor-action action to v0.8.1 ([38d6a12](https://github.com/jacaudi/authentik-operator/commit/38d6a12dc60a3a6589505b5ef718ac978338eebd))
* lint and test baseline (go-tricorder linters, -race, fuzzing) ([97b086d](https://github.com/jacaudi/authentik-operator/commit/97b086df06c9df3ab78b88bebcc9adc692c8ccf3))
* **lint:** adopt the go-tricorder stock-linter baseline ([167af0d](https://github.com/jacaudi/authentik-operator/commit/167af0d067ff205cc76e16373fd3445f1be0ac7b))
* post-[#1](https://github.com/jacaudi/authentik-operator/issues/1) hygiene (Navigator removal, toolchain go1.26.10, RBAC, dead code) ([c38825f](https://github.com/jacaudi/authentik-operator/commit/c38825f48f1869da4831a472cb25a7250590236d))
* **repo:** add yamllint/hadolint configs and managed ignore files ([20f8b5e](https://github.com/jacaudi/authentik-operator/commit/20f8b5e3672a9d1bc33e95fadb18a7c6727bcff1))
* **repo:** ignore docs/prompts/ ([b911c03](https://github.com/jacaudi/authentik-operator/commit/b911c031a6d701cea28387e79a59929d49905c70))
* **repo:** initialize with gitignore for refactor docs and source symlink ([d0673ec](https://github.com/jacaudi/authentik-operator/commit/d0673ec794c59b6d7afacda881cd73d221c26bbf))
* **repo:** remove Navigator tooling; move roadmap to docs/roadmap ([9106306](https://github.com/jacaudi/authentik-operator/commit/9106306beea27de899d3a652cfa0ca8428e6c954))
