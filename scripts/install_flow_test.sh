#!/usr/bin/env bash
set -euo pipefail

readonly script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)

fail() { printf 'install_flow_test: %s\n' "$*" >&2; exit 1; }

# Downloads stay in memory. require_root is stubbed and the installed program
# is never replaced, so these tests do not touch /usr/local/sbin.
fixture() {
  fixture_root=$(mktemp -d /tmp/proxydock-install-flow.XXXXXX)
  trap 'rm -rf -- "${fixture_root}"' EXIT
  unset PROXYDOCK_VERSION
  source "${script_dir}/install.sh"
  require_root() { return; }
  uname() { if [[ "$1" == -m ]]; then printf 'x86_64\n'; else printf 'Linux\n'; fi; }
  detect_current_version() { current_version=${fake_version:-unknown}; current_installed=${fake_installed:-false}; }
  printf '#!/usr/bin/env bash\nprintf "proxydock v1.2.3\\n"\n# release\n' > "${fixture_root}/release"
  release_checksum=$(sha256sum "${fixture_root}/release")
  release_checksum=${release_checksum%% *}
  asset_name=proxydock_linux_amd64_v1.2.3
  manifest_body=""
  download() {
    local url=$1 output=$2
    printf '%s\n' "${url}" >> "${fixture_root}/requests"
    case "${url}" in
      */releases/latest) printf '{"tag_name":"v1.2.3"}\n' > "${output}" ;;
      */SHA256SUMS) printf '%s' "${manifest_body}" > "${output}" ;;
      */"${asset_name}") cp -- "${fixture_root}/release" "${output}" ;;
      *) fail "unexpected download: ${url}" ;;
    esac
  }
  download_direct() { fail "release file download bypassed the proxy"; }
  printf -v manifest_body '%s  %s\n' "${release_checksum}" "${asset_name}"
}

run_installer() { ( main "$@" ); }

test_same_version_skips_release_files() (
  fixture
  fake_version=v1.2.3
  fake_installed=true
  download() {
    [[ "$1" == "${latest_release_api}" ]] || fail "same version downloaded ${1}"
    printf '{"tag_name":"v1.2.3"}\n' > "$2"
  }
  run_installer > /dev/null
)

test_downgrade_is_skipped() (
  fixture
  fake_version=v2.0.0
  fake_installed=true
  PROXYDOCK_VERSION=v1.2.3
  download() { fail "downgrade downloaded ${1}"; }
  local output
  output=$(run_installer)
  [[ "${output}" == *"已跳过以避免降级"* ]] || fail "downgrade was not skipped: ${output}"
)

test_checksum_failure_stops_before_replacement() (
  fixture
  fake_version=v1.2.2
  fake_installed=true
  PROXYDOCK_VERSION=v1.2.3
  printf -v manifest_body '%064d  %s\n' 0 "${asset_name}"
  install() { fail "checksum failure replaced the program"; }
  mv() { fail "checksum failure moved the program"; }
  local output
  if output=$(run_installer 2>&1); then fail "invalid checksum accepted"; fi
  [[ "${output}" == *"SHA-256 校验失败"* ]] || fail "wrong checksum error: ${output}"
)

test_asset_selection() (
  fixture
  local manifest="${fixture_root}/sums"
  printf '%s  proxydock_linux_amd64_v1.2.3\n' "${release_checksum}" > "${manifest}"
  select_asset "${manifest}" amd64
  [[ "${selected_asset}" == proxydock_linux_amd64_v1.2.3 ]] || fail "preferred asset not selected"

  printf '%s  proxydock_v1.2.3_linux_amd64\n' "${release_checksum}" > "${manifest}"
  select_asset "${manifest}" amd64
  [[ "${selected_asset}" == proxydock_v1.2.3_linux_amd64 ]] || fail "legacy asset not selected"

  printf '%s  proxydock_linux_amd64_v1.2.3\n%s  proxydock_v1.2.3_linux_amd64\n' \
    "${release_checksum}" "${release_checksum}" > "${manifest}"
  select_asset "${manifest}" amd64
  [[ "${selected_asset}" == proxydock_linux_amd64_v1.2.3 ]] || fail "legacy asset overrode the preferred name"

  printf '%s  proxydock_linux_amd64_v1.2.3\n%s  proxydock_linux_amd64_v1.2.4\n' \
    "${release_checksum}" "${release_checksum}" > "${manifest}"
  if ( select_asset "${manifest}" amd64 ) >/dev/null 2>&1; then fail "duplicate preferred assets accepted"; fi
)

test_version_must_match_selected_asset() (
  fixture
  fake_version=v1.2.2
  fake_installed=true
  PROXYDOCK_VERSION=v1.2.3
  printf -v manifest_body '%s  proxydock_linux_amd64_v9.9.9\n' "${release_checksum}"
  download() {
    case "$1" in
      */SHA256SUMS) printf '%s' "${manifest_body}" > "$2" ;;
      *) fail "downloaded ${1} after a version mismatch" ;;
    esac
  }
  local output
  if output=$(run_installer 2>&1); then fail "mismatched asset name accepted"; fi
  [[ "${output}" == *"二进制文件名不一致"* ]] || fail "wrong asset error: ${output}"
)

test_pinned_version_uses_environment() (
  fixture
  fake_version=v1.2.2
  fake_installed=true
  PROXYDOCK_VERSION=v1.2.3
  download() {
    printf '%s\n' "$1" >> "${fixture_root}/requests"
    return 1
  }
  if run_installer >/dev/null 2>&1; then fail "missing release was installed"; fi
  [[ $(cat "${fixture_root}/requests") == *'https://github.com/JokerKernel/proxydock/releases/download/v1.2.3/SHA256SUMS'* ]] || fail "pinned release was not requested"
)

test_same_version_skips_release_files
test_downgrade_is_skipped
test_checksum_failure_stops_before_replacement
test_asset_selection
test_version_must_match_selected_asset
test_pinned_version_uses_environment
printf 'install_flow_test: ok\n'
