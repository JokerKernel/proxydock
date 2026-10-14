#!/usr/bin/env bash
set -euo pipefail

readonly script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)

fail() { printf 'install_flow_test: %s\n' "$*" >&2; exit 1; }

# All filesystem changes stay in this fixture. Downloads are local copies and
# require_root is stubbed; these tests never install a live program or Docker.
fixture() {
  fixture_root=$(mktemp -d /tmp/proxydock-install-flow.XXXXXX)
  trap 'rm -rf -- "${fixture_root}"' EXIT
  export PROXYDOCK_INSTALL_DIR="${fixture_root}/bin"
  unset PROXYDOCK_VERSION PROXYDOCK_REPOSITORY
  source "${script_dir}/install.sh"
  require_root() { return; }
  uname() { if [[ "$1" == -m ]]; then printf 'x86_64\n'; else printf 'Linux\n'; fi; }
  mkdir -p "${install_dir}"
  printf '#!/usr/bin/env bash\nprintf "proxydock v1.2.3\\n"\n# release\n' > "${fixture_root}/release"
  release_checksum=$(sha256sum "${fixture_root}/release")
  release_checksum=${release_checksum%% *}
  manifest_extra=""
  download() {
    local url=$1 output=$2
    printf '%s\n' "${url}" >> "${fixture_root}/requests"
    case "${url}" in
      */releases/latest) printf '{"tag_name":"v1.2.3","prerelease":false,"draft":false}\n' > "${output}" ;;
      */SHA256SUMS)
        printf '%s  proxydock_linux_amd64_v1.2.3\n%s' "${release_checksum}" "${manifest_extra}" > "${output}"
        ;;
      */proxydock_linux_amd64_v1.2.3) cp -- "${fixture_root}/release" "${output}" ;;
      *) fail "unexpected download: ${url}" ;;
    esac
  }
  download_direct() { download "$@"; }
}

run_installer() { ( main "$@" ); }

current_binary() {
  printf '#!/usr/bin/env bash\nprintf "proxydock %s\\n"\n# original\n' "$1" > "${install_path}"
  chmod 0755 "${install_path}"
  original_checksum=$(sha256sum "${install_path}")
}

assert_original() {
  [[ $(sha256sum "${install_path}") == "${original_checksum}" ]] || fail "original program changed"
  local leftovers=()
  shopt -s nullglob
  leftovers=("${install_dir}"/.proxydock.new.* "${install_dir}"/.proxydock.old.*)
  [[ ${#leftovers[@]} -eq 0 ]] || fail "staged program or temporary backup leaked"
}

test_install_and_update() (
  fixture
  local output
  output=$(run_installer --version v1.2.3)
  [[ "$(${install_path} --version)" == 'proxydock v1.2.3' ]] || fail "fresh install has wrong version"
  [[ $(stat -c '%a' "${install_path}") == 755 ]] || fail "program has wrong permissions"
  [[ "${output}" == *"安装完成"* ]] || fail "missing install result"
  current_binary v1.2.2
  output=$(run_installer update --version v1.2.3)
  [[ "$(${install_path} --version)" == 'proxydock v1.2.3' ]] || fail "update did not replace program"
  [[ "${output}" == *"更新完成"* ]] || fail "missing update result"
)

test_latest_and_custom_repository() (
  fixture
  run_installer update --repository Example/proxydock > /dev/null
  [[ "$(${install_path} --version)" == 'proxydock v1.2.3' ]] || fail "latest update failed"
  [[ $(cat "${fixture_root}/requests") == *'https://api.github.com/repos/Example/proxydock/releases/latest'* ]] || fail "custom repository API was not used"
  [[ $(cat "${fixture_root}/requests") != *'JokerKernel'* ]] || fail "custom update downloaded from default repository"
)

test_same_version_and_downgrade_skip_downloads() (
  fixture
  download() { fail "no-op must not download an artifact"; }
  current_binary v1.2.3
  run_installer --version v1.2.3 > /dev/null
  assert_original
  current_binary v2.0.0
  run_installer update --version v1.2.3 --force > /dev/null
  assert_original
)

test_force_and_explicit_downgrade() (
  fixture
  current_binary v1.2.3
  run_installer update --version v1.2.3 --force > /dev/null
  [[ $(sha256sum "${install_path}") != "${original_checksum}" ]] || fail "--force did not reinstall"
  current_binary v2.0.0
  run_installer update --version v1.2.3 --allow-downgrade > /dev/null
  [[ "$(${install_path} --version)" == 'proxydock v1.2.3' ]] || fail "explicit downgrade failed"
)

test_checksum_failure_keeps_original() (
  fixture
  current_binary v1.2.2
  release_checksum=$(printf '%064d' 0)
  local output
  if output=$(run_installer update --version v1.2.3 2>&1); then fail "invalid checksum accepted"; fi
  [[ "${output}" == *"SHA-256 校验失败"* ]] || fail "wrong checksum error: ${output}"
  assert_original
)

test_invalid_or_wrong_version_binary_keeps_original() (
  for body in 'exit 1' 'printf "proxydock v9.0.0\n"'; do
    (
      fixture
      current_binary v1.2.2
      printf '#!/usr/bin/env bash\n%s\n' "${body}" > "${fixture_root}/release"
      release_checksum=$(sha256sum "${fixture_root}/release")
      release_checksum=${release_checksum%% *}
      if output=$(run_installer --version v1.2.3 2>&1); then fail "unusable program accepted"; fi
      [[ "${output}" == *"无法运行或版本不匹配"* ]] || fail "wrong artifact error: ${output}"
      assert_original
    )
  done
)

test_failed_post_install_verification_restores_original() (
  fixture
  current_binary v1.2.2
  verify_binary() { [[ "$1" != "${install_path}" ]]; }
  local output
  if output=$(run_installer update --version v1.2.3 2>&1); then fail "failed verification accepted"; fi
  [[ "${output}" == *"已恢复原 ProxyDock"* ]] || fail "rollback result missing: ${output}"
  assert_original
)

test_fresh_install_verification_failure_removes_incomplete_program() (
  fixture
  verify_binary() { [[ "$1" != "${install_path}" ]]; }
  if output=$(run_installer --version v1.2.3 2>&1); then fail "failed fresh install accepted"; fi
  [[ ! -e "${install_path}" ]] || fail "incomplete fresh installation retained"
)

test_interrupt_during_replacement_restores_original() (
  fixture
  current_binary v1.2.2
  verify_binary() {
    if [[ "$1" == "${install_path}" ]]; then kill -TERM "${BASHPID}"; fi
    return 0
  }
  if output=$(run_installer --version v1.2.3 2>&1); then fail "interrupted install succeeded"; fi
  assert_original
)

test_failed_atomic_replace_restores_original() (
  fixture
  current_binary v1.2.2
  mv() { if [[ "$3" == *'.proxydock.new.'* ]]; then return 1; fi; command mv "$@"; }
  if output=$(run_installer --version v1.2.3 2>&1); then fail "failed replacement accepted"; fi
  assert_original
)

test_bad_manifests_never_replace_original() (
  for kind in duplicate traversal wrong-version; do
    (
      fixture
      current_binary v1.2.2
      case "${kind}" in
        duplicate) manifest_extra="${release_checksum}  proxydock_linux_amd64_v1.2.3" ;;
        traversal | wrong-version)
          download() {
            [[ "$1" == */SHA256SUMS ]] || fail "downloaded program from invalid manifest"
            local name='../../proxydock_linux_amd64_v1.2.3'
            [[ "${kind}" != wrong-version ]] || name=proxydock_linux_amd64_v9.9.9
            printf '%s  %s\n' "${release_checksum}" "${name}" > "$2"
          }
          ;;
      esac
      if output=$(run_installer --version v1.2.3 2>&1); then fail "${kind} manifest accepted"; fi
      assert_original
    )
  done
)

test_lock_prevents_concurrent_update() (
  fixture
  current_binary v1.2.2
  exec 8>"${install_dir}/.proxydock.install.lock"
  flock -n 8
  if output=$(run_installer update --version v1.2.3 2>&1); then fail "concurrent update accepted"; fi
  [[ "${output}" == *"另一个 ProxyDock"* ]] || fail "wrong lock error: ${output}"
  assert_original
)

test_uninstall_confirmation_and_data_retention() (
  fixture
  current_binary v1.2.2
  printf 'node credentials\n' > "${fixture_root}/node.json"
  download() { fail "uninstall must not access the network"; }
  if output=$(run_installer uninstall < /dev/null 2>&1); then fail "unconfirmed uninstall accepted"; fi
  [[ "${output}" == *"--yes"* ]] || fail "confirmation requirement missing"
  assert_original
  run_installer uninstall --yes > /dev/null
  [[ ! -e "${install_path}" ]] || fail "program not removed"
  [[ $(cat "${fixture_root}/node.json") == 'node credentials' ]] || fail "uninstall changed node data"
  run_installer --uninstall --yes > /dev/null
)

test_invalid_configuration_is_rejected() (
  for kind in repository version symlink directory; do
    (
      fixture
      current_binary v1.2.2
      local args=(--version v1.2.3)
      case "${kind}" in
        repository) args+=(--repository ../bad) ;;
        version) args=(--version 'v1.2.3;echo bad') ;;
        symlink) mv "${install_path}" "${fixture_root}/original"; ln -s "${fixture_root}/original" "${install_path}" ;;
        directory) rm "${install_path}"; mkdir "${install_path}" ;;
      esac
      download() { fail "invalid configuration accessed the network"; }
      if output=$(run_installer "${args[@]}" 2>&1); then fail "${kind} accepted"; fi
      if [[ "${kind}" == symlink ]]; then
        [[ $(sha256sum "${fixture_root}/original") == "${original_checksum/"${install_path}"/"${fixture_root}/original"}" ]] || fail "symlink target changed"
      elif [[ "${kind}" != directory ]]; then
        assert_original
      fi
    )
  done
)

test_semantic_version_order() (
  fixture
  local cases=(
    'v1.0.0-alpha v1.0.0-alpha.1 -1'
    'v1.0.0-alpha.1 v1.0.0-alpha.beta -1'
    'v1.0.0-beta.2 v1.0.0-beta.11 -1'
    'v1.0.0-rc.1 v1.0.0 -1'
    'v1.0.0+build.1 v1.0.0+build.2 0'
    'v999999999999999999999.0.0 v1000000000000000000000.0.0 -1'
    'v2.0.0 v1.999.999 1'
  )
  local entry left right want
  for entry in "${cases[@]}"; do
    read -r left right want <<< "${entry}"
    compare_versions "${left}" "${right}"
    [[ ${version_order} -eq ${want} ]] || fail "wrong comparison: ${entry}: ${version_order}"
  done
  for entry in v01.0.0 v1.0.0-01 v1.0.0- v1.0.0+; do
    if valid_version "${entry}"; then fail "invalid version accepted: ${entry}"; fi
  done
)

test_help_skips_root_and_environment_detection() (
  fixture
  require_root() { fail "help checked root"; }
  uname() { fail "help detected environment"; }
  output=$(run_installer --help)
  [[ "${output}" == *'JokerKernel/proxydock'* && "${output}" == *'--allow-downgrade'* ]] || fail "incomplete installer help"
)

test_install_and_update
test_latest_and_custom_repository
test_same_version_and_downgrade_skip_downloads
test_force_and_explicit_downgrade
test_checksum_failure_keeps_original
test_invalid_or_wrong_version_binary_keeps_original
test_failed_post_install_verification_restores_original
test_fresh_install_verification_failure_removes_incomplete_program
test_interrupt_during_replacement_restores_original
test_failed_atomic_replace_restores_original
test_bad_manifests_never_replace_original
test_lock_prevents_concurrent_update
test_uninstall_confirmation_and_data_retention
test_invalid_configuration_is_rejected
test_semantic_version_order
test_help_skips_root_and_environment_detection
printf 'install_flow_test: ok\n'
