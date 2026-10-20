#!/usr/bin/env bash
set -euo pipefail

readonly test_root=$(mktemp -d /tmp/proxydock-installer-tests.XXXXXX)
trap 'rm -rf -- "${test_root}"' EXIT

readonly script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)

fail() {
  printf 'install_test: %s\n' "$*" >&2
  exit 1
}

test_automatic_mode_skips_when_already_current() (
  source "${script_dir}/install.sh"
  require_root() { return; }
  detect_current_version() { current_version=v1.2.3; current_installed=true; }
  download() {
    local url=$1 output=$2
    [[ "${url}" == "${latest_release_api}" ]] || fail "unexpected download: ${url}"
    printf '{"tag_name":"v1.2.3"}\n' > "${output}"
  }
  download_direct() { fail "direct download used for the current version"; }

  local output
  output=$(main)
  [[ "${output}" == *"╭─ ProxyDock"* ]] || fail "missing installer header"
  [[ "${output}" == *"当前版本"* && "${output}" == *"v1.2.3"* ]] || fail "missing current version summary"
  [[ "${output}" == *"目标版本"* && "${output}" == *"v1.2.3"* ]] || fail "missing target version summary"
  [[ "${output}" == *"当前已是最新正式版本，无需更新。"* ]] || fail "missing up-to-date result"
  [[ "${output}" == *"更新结果"* ]] || fail "missing update result label"
  [[ "${output}" != *$'\033['* ]] || fail "redirected output contains ANSI controls"
  [[ "${output}" != *"SHA256SUMS"* ]] || fail "downloaded release files for current version"
)

test_color_controls() (
  PROXYDOCK_COLOR=always
  unset NO_COLOR
  source "${script_dir}/install.sh"

  local output
  output=$(info "彩色信息" "详情")
  [[ "${output}" == *$'\033[34m[信息]\033[0m'* ]] || fail "blue information color was not applied"
  [[ "${output}" == *$'\033[0m 彩色信息：详情'* ]] || fail "information text was mixed with the colored badge: ${output}"
  output=$(print_header)
  [[ "${output}" == *$'\033[1;38;5;208m╭─ ProxyDock\033[0m'* ]] || fail "orange title color was not applied"
  output=$(step "彩色步骤" "详情")
  [[ "${output}" == *$'\033[1;38;5;208m[步骤]\033[0m'* ]] || fail "orange step color was not applied"
  output=$(result "安装结果" "安装完成")
  [[ "${output}" == *$'\033[1;32m安装完成\033[0m'* ]] || fail "green result color was not applied"
)

test_no_color_is_respected() (
  unset PROXYDOCK_COLOR
  NO_COLOR=""
  source "${script_dir}/install.sh"

  local output
  output=$(result "纯文本结果" "详情")
  [[ "${output}" != *$'\033['* && "${output}" == *"纯文本结果"* && "${output}" == *"│ 详情"* ]] || fail "NO_COLOR output was decorated: ${output}"
)

test_version_comparison_and_classification() (
  source "${script_dir}/install.sh"

  compare_versions v1.2.2 v1.2.3
  [[ ${version_order} -eq -1 ]] || fail "older patch version was not lower"
  compare_versions v1.2.3-dirty v1.2.3
  [[ ${version_order} -eq -1 ]] || fail "prerelease was not lower than release"
  compare_versions v2.0.0 v1.9.9
  [[ ${version_order} -eq 1 ]] || fail "newer major version was not higher"
  compare_versions v1.0.0-beta.2 v1.0.0-beta.11
  [[ ${version_order} -eq 1 ]] || fail "prerelease labels were not compared as text"
  compare_versions v1.0.0+build.1 v1.0.0+build.2
  [[ ${version_order} -eq 0 ]] || fail "build metadata changed version order"

  detect_current_version() { current_version=v1.2.2; current_installed=true; }
  resolved_version=v1.2.3
  classify_installation
  [[ "${installation_action}" == "upgrade" ]] || fail "older installation was not classified as upgrade"
)

test_arguments() (
  source "${script_dir}/install.sh"
  parse_arguments --yes uninstall
  [[ "${requested_action}" == uninstall ]] || fail "uninstall argument not parsed"
  requested_action=install
  parse_arguments --uninstall --yes
  [[ "${requested_action}" == uninstall ]] || fail "--uninstall argument not parsed"

  local output
  if output=$(parse_arguments update 2>&1); then fail "update action accepted"; fi
  [[ "${output}" == *"未知参数"* ]] || fail "wrong argument error: ${output}"
  if output=$(parse_arguments --version v1.2.3 2>&1); then fail "version flag accepted"; fi
  [[ "${output}" == *"未知参数"* ]] || fail "version flag error: ${output}"
)

test_uninstall_removes_only_proxydock_binary() (
  source "${script_dir}/install.sh"
  require_root() { return; }
  local installed=true
  proxydock_binary_exists() { [[ "${installed}" == true ]]; }
  remove_proxydock_binary() { installed=false; }

  local output
  output=$(main uninstall)
  [[ "${output}" == *"自身卸载"* ]] || fail "missing uninstall header"
  [[ "${output}" == *"ProxyDock 自身已卸载。"* ]] || fail "missing uninstall result"
  [[ "${output}" == *"Docker 容器、镜像、节点配置和管理数据均已保留。"* ]] || fail "missing retained data notice"
)

test_uninstall_is_idempotent() (
  source "${script_dir}/install.sh"
  require_root() { return; }
  proxydock_binary_exists() { return 1; }
  remove_proxydock_binary() { fail "remove called for absent binary"; }

  local output
  output=$(main uninstall --yes)
  [[ "${output}" == *"当前未安装，无需卸载"* ]] || fail "missing already-uninstalled result"
)

test_version_file_fallback() (
  source "${script_dir}/install.sh"
  require_root() { return; }
  detect_current_version() { current_version=v1.2.3; current_installed=true; }
  download() {
    local url=$1 output=$2
    case "${url}" in
      "${latest_release_api}")
        printf '{"tag_name":"latest"}\n' > "${output}"
        ;;
      */version)
        printf 'v1.2.3\n' > "${output}"
        ;;
      *)
        fail "unexpected fallback download: ${url}"
        ;;
    esac
  }
  download_direct() { return 1; }

  local output
  output=$(main)
  [[ "${output}" == *"切换版本来源"* && "${output}" == *"GitHub Releases API 不可用，改用 Release version 文件"* ]] || fail "missing API fallback status"
  [[ "${output}" == *"v1.2.3（Release version 文件）"* ]] || fail "version fallback failed"
)

test_api_failure_retries_without_proxy() (
  source "${script_dir}/install.sh"
  require_root() { return; }
  detect_current_version() { current_version=v1.2.3; current_installed=true; }
  download() {
    [[ "$1" == "${latest_release_api}" ]] || fail "unexpected proxied download: $1"
    return 1
  }
  download_direct() {
    local url=$1 output=$2
    [[ "${url}" == "${latest_release_api}" ]] || fail "unexpected direct download: ${url}"
    printf '{"tag_name":"v1.2.3"}\n' > "${output}"
  }

  local output
  output=$(main)
  [[ "${output}" == *"切换下载方式"* && "${output}" == *"尝试忽略代理直连"* ]] || fail "missing direct retry notice"
  [[ "${output}" == *"v1.2.3（GitHub Releases API 直连）"* ]] || fail "direct API retry failed"
  [[ "${output}" != *"改用 Release version 文件"* ]] || fail "version fallback ran after successful direct retry"
)

test_release_files_do_not_bypass_proxy() (
  source "${script_dir}/install.sh"
  require_root() { return; }
  detect_current_version() { current_version=v1.2.2; current_installed=true; }
  download() {
    local url=$1 output=$2
    case "${url}" in
      "${latest_release_api}") printf '{"tag_name":"v1.2.3"}\n' > "${output}" ;;
      *) return 1 ;;
    esac
  }
  download_direct() { printf 'direct\n' > "${test_root}/direct"; return 1; }

  # die calls exit, so main must run in its own subshell or it ends this test.
  if ( main >/dev/null 2>&1 ); then fail "release download failure was ignored"; fi
  [[ ! -e "${test_root}/direct" ]] || fail "checksum or program download bypassed the proxy"
)

test_curl_direct_download_bypasses_all_proxies() (
  source "${script_dir}/install.sh"
  local captured=()
  curl() { captured=("$@"); printf '{}\n' > "${test_root}/api"; }

  download_direct "${latest_release_api}" "${test_root}/api"
  local index found=false
  for ((index = 0; index + 1 < ${#captured[@]}; index++)); do
    if [[ "${captured[index]}" == "--noproxy" && "${captured[index + 1]}" == "*" ]]; then
      found=true
      break
    fi
  done
  [[ "${found}" == true ]] || fail "curl direct retry did not use --noproxy '*'"
)

test_automatic_mode_skips_when_already_current
test_color_controls
test_no_color_is_respected
test_version_comparison_and_classification
test_arguments
test_uninstall_removes_only_proxydock_binary
test_uninstall_is_idempotent
test_api_failure_retries_without_proxy
test_release_files_do_not_bypass_proxy
test_curl_direct_download_bypasses_all_proxies
test_version_file_fallback
printf 'install_test: ok\n'
