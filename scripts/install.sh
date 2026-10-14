#!/usr/bin/env bash
set -euo pipefail

repository=${PROXYDOCK_REPOSITORY:-JokerKernel/proxydock}
readonly install_dir="${PROXYDOCK_INSTALL_DIR:-/usr/local/sbin}"
readonly install_path="${install_dir}/proxydock"
readonly max_version_size=256
readonly max_release_api_size=$((1024 * 1024))
readonly max_binary_size=$((128 * 1024 * 1024))
export LC_ALL=C
latest_release_api="https://api.github.com/repos/${repository}/releases/latest"

color_enabled=false
proxydock_color_mode=${PROXYDOCK_COLOR:-auto}
proxydock_color_mode=${proxydock_color_mode,,}
case "${proxydock_color_mode}" in
  always | 1 | true | yes)
    color_enabled=true
    ;;
  never | 0 | false | no)
    ;;
  *)
    if [[ -z "${NO_COLOR+x}" && "${TERM:-}" != "dumb" && -t 1 ]]; then
      color_enabled=true
    fi
    ;;
esac
readonly color_enabled
readonly proxydock_color_mode

if [[ "${color_enabled}" == true ]]; then
  readonly color_reset=$'\033[0m'
  readonly color_bold_orange=$'\033[1;38;5;208m'
  readonly color_orange=$'\033[38;5;208m'
  readonly color_blue=$'\033[34m'
  readonly color_bold_green=$'\033[1;32m'
  readonly color_bold_yellow=$'\033[1;33m'
  readonly color_bold_red=$'\033[1;31m'
else
  readonly color_reset=""
  readonly color_bold_orange=""
  readonly color_orange=""
  readonly color_blue=""
  readonly color_bold_green=""
  readonly color_bold_yellow=""
  readonly color_bold_red=""
fi

current_version=""
current_installed=false
requested_action="install"
requested_version=${PROXYDOCK_VERSION:-latest}
assume_yes=false
force_install=false
allow_downgrade=false

selected_asset=""
selected_checksum=""
temporary_dir=""
staged_path=""
backup_path=""
replacement_pending=false
lock_acquired=false

paint() {
  printf '%s%s%s' "$1" "$2" "${color_reset}"
}

info() {
  printf '%s %s\n' "$(paint "${color_blue}" '[信息]')" "$*"
}

step() {
  printf '\n%s %s\n' "$(paint "${color_bold_orange}" '[步骤]')" "$*"
}

result() {
  printf '%s %s\n' "$(paint "${color_bold_green}" '[结果]')" "$*"
}

warn() {
  printf '%s %s\n' "$(paint "${color_bold_yellow}" '[警告]')" "$*"
}

die() {
  printf '%s %s\n' "$(paint "${color_bold_red}" '[错误]')" "$*" >&2
  exit 1
}

print_header() {
  local subtitle=${1:-自身安装与更新}
  printf '%s\n' "$(paint "${color_bold_orange}" '╭─ ProxyDock')"
  printf '%s\n' "$(paint "${color_orange}" "│ ${subtitle}")"
  printf '%s\n' "$(paint "${color_bold_orange}" '╰──────────────────────────────────────────────')"
}

print_release_summary() {
  local current_display="未安装"
  local action_display="安装"
  if [[ "${current_installed}" == true ]]; then
    current_display=${current_version}
  fi
  case "${installation_action}" in
    upgrade) action_display="更新" ;;
    replace) action_display="重新安装" ;;
    downgrade) action_display="降级" ;;
    current) action_display="无需更新" ;;
    newer) action_display="跳过，避免降级" ;;
  esac
  printf '\n%s\n' "$(paint "${color_bold_orange}" '发布信息')"
  printf '%s\n' "$(paint "${color_orange}" '----------------------------------------')"
  printf '  %s：Linux/%s\n' "$(paint "${color_orange}" '平台')" "$1"
  printf '  %s：%s\n' "$(paint "${color_orange}" '当前版本')" "${current_display}"
  printf '  %s：%s\n' "$(paint "${color_orange}" '目标版本')" "${resolved_version}"
  printf '  %s：%s\n' "$(paint "${color_orange}" '执行操作')" "${action_display}"
  printf '%s\n' "$(paint "${color_orange}" '----------------------------------------')"
}

print_completion() {
  local action_label="安装完成"
  if [[ "${installation_action}" == "upgrade" ]]; then
    action_label="更新完成"
  elif [[ "${installation_action}" == "downgrade" ]]; then
    action_label="降级完成"
  elif [[ "${installation_action}" == "replace" ]]; then
    action_label="重新安装完成"
  fi
  printf '\n%s\n' "$(paint "${color_bold_orange}" '╭─ ProxyDock')"
  printf '%s│ %s%s%s\n' "${color_orange}" "${color_bold_green}" "${action_label}" "${color_reset}"
  printf '%s\n' "$(paint "${color_orange}" "│ 版本：${resolved_version}")"
  printf '%s\n' "$(paint "${color_orange}" "│ 位置：${install_path}")"
  printf '%s\n' "$(paint "${color_orange}" "│ 运行命令：sudo ${install_path}")"
  printf '%s\n' "$(paint "${color_bold_orange}" '╰──────────────────────────────────────────────')"
}

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  if [[ "${replacement_pending}" == true ]]; then
    if [[ -n "${backup_path}" ]]; then
      if mv -f -- "${backup_path}" "${install_path}"; then
        backup_path=""
        warn "安装未完成，已恢复原 ProxyDock 程序。"
      else
        warn "恢复失败，原程序备份保留在：${backup_path}"
        backup_path=""
        status=1
      fi
    elif ! rm -f -- "${install_path}"; then
      warn "删除未完成安装的程序失败：${install_path}"
      status=1
    fi
  fi
  [[ -z "${staged_path}" ]] || rm -f -- "${staged_path}"
  [[ -z "${backup_path}" ]] || rm -f -- "${backup_path}"
  [[ -z "${temporary_dir}" ]] || rm -rf -- "${temporary_dir}"
  if [[ "${lock_acquired}" == true ]]; then
    exec 9>&-
  fi
  return "${status}"
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "缺少必需命令：$1"
}

require_root() {
  [[ ${EUID} -eq 0 ]] || die "请使用 root 运行，例如：curl ... | sudo bash"
}

proxydock_binary_exists() {
  [[ -e "${install_path}" || -L "${install_path}" ]]
}

remove_proxydock_binary() {
  [[ ! -d "${install_path}" ]] || die "卸载目标是目录，拒绝删除：${install_path}"
  rm -f -- "${install_path}"
}

uninstall_proxydock() {
  print_header "自身卸载"
  step "检查 ProxyDock 安装状态"
  if ! proxydock_binary_exists; then
    result "ProxyDock 当前未安装，无需卸载。"
    return
  fi

  confirm_uninstall
  info "卸载位置：${install_path}"
  step "删除 ProxyDock 二进制"
  remove_proxydock_binary
  if proxydock_binary_exists; then
    die "卸载后仍检测到文件：${install_path}"
  fi
  result "ProxyDock 自身已卸载。"
  info "Docker 容器、镜像、节点配置和管理数据均已保留。"
}

parse_arguments() {
  local action_seen=false
  while [[ $# -gt 0 ]]; do
    case "$1" in
      install | update | --update | uninstall | --uninstall)
        [[ "${action_seen}" == false ]] || die "只能指定一个操作：install、update 或 uninstall"
        requested_action=${1#--}
        action_seen=true
        shift
        ;;
      --yes | -y) assume_yes=true; shift ;;
      --force) force_install=true; shift ;;
      --allow-downgrade) allow_downgrade=true; shift ;;
      --version | --repository)
        [[ $# -ge 2 && -n "$2" && "$2" != --* ]] || die "$1 需要一个值"
        if [[ "$1" == "--version" ]]; then
          requested_version=$2
        else
          repository=$2
        fi
        shift 2
        ;;
      --help | -h) requested_action="help"; shift ;;
      *) die "未知参数：$1" ;;
    esac
  done
}

detect_current_version() {
  if [[ -n "${current_version}" ]]; then
    return
  fi
  if [[ -x "${install_path}" ]]; then
    current_installed=true
    local version_output
    version_output=$("${install_path}" --version 2>/dev/null || true)
    current_version=${version_output%%$'\n'*}
    current_version=${current_version#proxydock }
  fi
  if [[ -z "${current_version}" ]]; then
    current_version="unknown"
  fi
}

compare_versions() {
  local left=$1 right=$2
  valid_version "${left}" && valid_version "${right}" || return 1
  left=${left#v}
  right=${right#v}
  left=${left%%+*}
  right=${right%%+*}
  local left_core=${left%%-*} right_core=${right%%-*}
  local left_parts=() right_parts=() index
  IFS=. read -r -a left_parts <<< "${left_core}"
  IFS=. read -r -a right_parts <<< "${right_core}"
  for index in 0 1 2; do
    compare_numeric "${left_parts[index]}" "${right_parts[index]}"
    if [[ ${version_order} -ne 0 ]]; then return; fi
  done
  local left_pre="" right_pre=""
  [[ "${left}" != *-* ]] || left_pre=${left#*-}
  [[ "${right}" != *-* ]] || right_pre=${right#*-}
  if [[ -z "${left_pre}" && -z "${right_pre}" ]]; then
    version_order=0
  elif [[ -z "${left_pre}" ]]; then
    version_order=1
  elif [[ -z "${right_pre}" ]]; then
    version_order=-1
  else
    IFS=. read -r -a left_parts <<< "${left_pre}"
    IFS=. read -r -a right_parts <<< "${right_pre}"
    for ((index = 0; index < ${#left_parts[@]} && index < ${#right_parts[@]}; index++)); do
      local a=${left_parts[index]} b=${right_parts[index]}
      if [[ "${a}" == "${b}" ]]; then continue; fi
      if [[ "${a}" =~ ^[0-9]+$ && "${b}" =~ ^[0-9]+$ ]]; then
        compare_numeric "${a}" "${b}"
      elif [[ "${a}" =~ ^[0-9]+$ ]]; then
        version_order=-1
      elif [[ "${b}" =~ ^[0-9]+$ ]]; then
        version_order=1
      elif [[ "${a}" < "${b}" ]]; then
        version_order=-1
      else
        version_order=1
      fi
      return
    done
    compare_numeric "${#left_parts[@]}" "${#right_parts[@]}"
  fi
}

classify_installation() {
  installation_action="install"
  detect_current_version
  if [[ "${current_installed}" != true && "${current_version}" != "unknown" ]]; then
    current_installed=true
  fi
  if [[ "${current_installed}" != true ]]; then
    return
  fi
  if ! compare_versions "${current_version}" "${resolved_version}"; then
    installation_action="replace"
    return
  fi
  case "${version_order}" in
    -1) installation_action="upgrade" ;;
    0) installation_action="current" ;;
    1) installation_action="newer" ;;
  esac
}

detect_architecture() {
  case "$(uname -m)" in
    x86_64 | amd64)
      printf 'amd64\n'
      ;;
    aarch64 | arm64)
      printf 'arm64\n'
      ;;
    *)
      die "不支持的处理器架构：$(uname -m)（仅支持 amd64、arm64）"
      ;;
  esac
}

download() {
  local url=$1 output=$2 show_progress=${3:-false} limit=${4:-${max_binary_size}}
  if command -v curl >/dev/null 2>&1; then
    local presentation=(--silent --show-error)
    if [[ "${show_progress}" == true && -t 2 ]]; then presentation=(--show-error --progress-bar); fi
    curl --proto '=https' --proto-redir '=https' --tlsv1.2 --fail --location \
      "${presentation[@]}" --connect-timeout 15 --max-time 180 --retry 3 --retry-max-time 300 \
      --max-filesize "${limit}" --output "${output}" "${url}" || return $?
  elif command -v wget >/dev/null 2>&1; then
    wget --https-only --secure-protocol=TLSv1_2 --quiet --timeout=30 --tries=3 \
      --output-document="${output}" "${url}" || return $?
  else
    die "需要 curl 或 wget 才能下载发布文件"
  fi
  [[ $(wc -c < "${output}") -le ${limit} ]] || { warn "下载文件超过大小限制"; return 1; }
}

download_direct() {
  local url=$1 output=$2 show_progress=${3:-false} limit=${4:-${max_binary_size}}
  if command -v curl >/dev/null 2>&1; then
    local presentation=(--silent --show-error)
    if [[ "${show_progress}" == true && -t 2 ]]; then presentation=(--show-error --progress-bar); fi
    curl --proto '=https' --proto-redir '=https' --tlsv1.2 --fail --location \
      "${presentation[@]}" --connect-timeout 15 --max-time 180 --retry 3 --retry-max-time 300 \
      --max-filesize "${limit}" --noproxy '*' --output "${output}" "${url}" || return $?
  elif command -v wget >/dev/null 2>&1; then
    wget --https-only --secure-protocol=TLSv1_2 --quiet --timeout=30 --tries=3 --no-proxy \
      --output-document="${output}" "${url}" || return $?
  else
    die "需要 curl 或 wget 才能下载发布文件"
  fi
  [[ $(wc -c < "${output}") -le ${limit} ]] || { warn "下载文件超过大小限制"; return 1; }
}

read_api_version() {
  local api_file=$1
  local api_size
  api_size=$(wc -c < "${api_file}")
  [[ ${api_size} -le ${max_release_api_size} ]] || return 1

  local versions=()
  mapfile -t versions < <(
    sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "${api_file}"
  )
  [[ ${#versions[@]} -eq 1 ]] || return 1
  valid_version "${versions[0]}" || return 1
  resolved_version=${versions[0]}
}

read_version_file() {
  local version_file=$1
  local version_size
  version_size=$(wc -c < "${version_file}")
  [[ ${version_size} -le ${max_version_size} ]] || return 1

  local version_lines=()
  mapfile -t version_lines < "${version_file}"
  [[ ${#version_lines[@]} -eq 1 ]] || return 1
  valid_version "${version_lines[0]}" || return 1
  resolved_version=${version_lines[0]}
}

resolve_latest_version() {
  local release_base=$1
  local api_file="${temporary_dir}/latest-release.json"
  local version_file="${temporary_dir}/version"

  if download "${latest_release_api}" "${api_file}" false "${max_release_api_size}" && read_api_version "${api_file}"; then
    info "最新正式版本：${resolved_version}（GitHub Releases API）"
    return
  fi
  warn "通过当前网络或代理获取 GitHub Releases API 失败，尝试忽略代理直连"
  if download_direct "${latest_release_api}" "${api_file}" false "${max_release_api_size}" && read_api_version "${api_file}"; then
    info "最新正式版本：${resolved_version}（GitHub Releases API 直连）"
    return
  fi
  warn "GitHub Releases API 不可用，改用 Release version 文件"
  if download "${release_base}/version" "${version_file}" false "${max_version_size}" && read_version_file "${version_file}"; then
    info "最新正式版本：${resolved_version}（Release version 文件）"
    return
  fi
  return 1
}

select_asset() {
  local manifest=$1 architecture=$2 checksum asset extra
  [[ $(wc -c < "${manifest}") -le ${max_release_api_size} ]] || die "校验清单超过大小限制"
  local preferred="proxydock_linux_${architecture}_${resolved_version}"
  local legacy="proxydock_${resolved_version}_linux_${architecture}"
  local preferred_matches=0 legacy_matches=0 preferred_checksum="" legacy_checksum=""
  while read -r checksum asset extra || [[ -n "${checksum}" ]]; do
    [[ -z "${extra:-}" && "${checksum}" =~ ^[0-9A-Fa-f]{64}$ ]] || continue
    asset=${asset#\*}
    if [[ "${asset}" == "${preferred}" ]]; then
      preferred_checksum=${checksum,,}
      preferred_matches=$((preferred_matches + 1))
    elif [[ "${asset}" == "${legacy}" ]]; then
      legacy_checksum=${checksum,,}
      legacy_matches=$((legacy_matches + 1))
    fi
  done < "${manifest}"
  [[ ${preferred_matches} -le 1 && ${legacy_matches} -le 1 ]] || die "校验清单包含重复的发布文件"
  if [[ ${preferred_matches} -eq 1 ]]; then
    selected_asset=${preferred}
    selected_checksum=${preferred_checksum}
  elif [[ ${legacy_matches} -eq 1 ]]; then
    selected_asset=${legacy}
    selected_checksum=${legacy_checksum}
  else
    die "校验清单缺少 ${resolved_version} Linux/${architecture} 的 ProxyDock 发布文件"
  fi
}


print_help() {
  cat <<'HELP'
ProxyDock 程序安装、更新与卸载

用法：bash install.sh [install|update|uninstall] [选项]
  --version VERSION       固定程序版本，例如 v1.0.0；缺省 latest
  --repository OWNER/REPO 发布仓库；缺省 JokerKernel/proxydock
  --force                 同版本重新安装
  --allow-downgrade       允许显式降级
  --yes, -y               跳过程序卸载确认
  --help, -h              查看帮助，不需要 root

环境变量：PROXYDOCK_VERSION、PROXYDOCK_REPOSITORY、PROXYDOCK_INSTALL_DIR
默认安装位置：/usr/local/sbin/proxydock
安装和更新只替换管理程序；卸载保留 Docker、容器、镜像、配置与数据。
HELP
}

valid_version() {
  local value=$1 identifier
  [[ ${#value} -le ${max_version_size} && "${value}" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$ ]] || return 1
  value=${value%%+*}
  if [[ "${value}" == *-* ]]; then
    local identifiers=()
    IFS=. read -r -a identifiers <<< "${value#*-}"
    for identifier in "${identifiers[@]}"; do
      if [[ "${identifier}" =~ ^[0-9]+$ && ${#identifier} -gt 1 && "${identifier}" == 0* ]]; then return 1; fi
    done
  fi
}

compare_numeric() {
  if [[ ${#1} -lt ${#2} ]]; then version_order=-1
  elif [[ ${#1} -gt ${#2} ]]; then version_order=1
  elif [[ "$1" == "$2" ]]; then version_order=0
  elif [[ "$1" < "$2" ]]; then version_order=-1
  else version_order=1
  fi
}

acquire_install_lock() {
  install -d -m 0755 "${install_dir}" || die "无法创建安装目录：${install_dir}"
  local lock_path="${install_dir}/.proxydock.install.lock"
  [[ ! -L "${lock_path}" && ( ! -e "${lock_path}" || -f "${lock_path}" ) ]] || die "安装锁路径无效"
  # Create private temporary/lock files without changing the executable mode.
  umask 077
  exec 9>"${lock_path}" || die "无法打开安装锁：${lock_path}"
  flock -n 9 || die "另一个 ProxyDock 安装、更新或卸载正在进行，请稍后重试"
  lock_acquired=true
}

fetch() {
  if download "$@"; then return; fi
  warn "通过当前网络下载失败，尝试忽略代理直连"
  download_direct "$@"
}

verify_binary() {
  local output first_line
  output=$(timeout 10 "$1" --version 2>/dev/null) || return 1
  first_line=${output%%$'\n'*}
  [[ "${first_line}" == "proxydock ${resolved_version}" ]]
}

confirm_uninstall() {
  [[ "${assume_yes}" == true ]] && return
  [[ -t 0 ]] || die "非交互卸载必须显式提供 --yes"
  local answer
  printf '只删除 ProxyDock 程序，保留容器、镜像和节点数据。确认卸载？[y/N]：'
  read -r answer || die "未收到卸载确认"
  [[ "${answer}" == y || "${answer}" == Y || "${answer}" == 1 ]] || die "已取消卸载"
}

main() {
  requested_action="install"
  current_version=""
  current_installed=false
  requested_version=${PROXYDOCK_VERSION:-latest}
  assume_yes=false
  force_install=false
  allow_downgrade=false
  parse_arguments "$@"
  if [[ "${requested_action}" == "help" ]]; then print_help; return; fi
  [[ "$(uname -s)" == "Linux" ]] || die "该安装脚本仅支持 Linux"
  require_root
  [[ "${install_dir}" == /* && "${install_dir}" != *$'\n'* ]] || die "安装目录必须是绝对路径"
  [[ ! -L "${install_path}" && ( ! -e "${install_path}" || -f "${install_path}" ) ]] || die "安装目标不是普通文件，拒绝操作：${install_path}"
  require_command rm
  if [[ "${requested_action}" == "uninstall" && ! -d "${install_dir}" ]]; then
    uninstall_proxydock
    return
  fi
  require_command install
  require_command flock
  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  acquire_install_lock
  if [[ "${requested_action}" == "uninstall" ]]; then
    uninstall_proxydock
    return
  fi
  [[ "${repository}" =~ ^[A-Za-z0-9][A-Za-z0-9_-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$ ]] || die "仓库格式必须是 OWNER/REPO"
  latest_release_api="https://api.github.com/repos/${repository}/releases/latest"
  [[ "${requested_version}" == "latest" ]] || valid_version "${requested_version}" || die "版本格式无效：${requested_version}"
  local dependency
  for dependency in uname mktemp sha256sum wc sed mv cp chmod timeout; do require_command "${dependency}"; done
  local architecture release_base
  architecture=$(detect_architecture) || die "无法选择当前平台的程序"
  resolved_version=""
  if [[ "${requested_version}" == "latest" ]]; then
    release_base="https://github.com/${repository}/releases/latest/download"
  else
    resolved_version=${requested_version}
    release_base="https://github.com/${repository}/releases/download/${resolved_version}"
  fi
  temporary_dir=$(mktemp -d /tmp/proxydock-install.XXXXXX) || die "无法创建下载临时目录"
  print_header
  info "发布仓库：${repository}"
  step "检查发布版本"
  if [[ "${requested_version}" == "latest" ]]; then
    resolve_latest_version "${release_base}" || die "无法读取最新正式版本；请确认仓库已有 Release，或使用 --version 指定版本"
    release_base="https://github.com/${repository}/releases/download/${resolved_version}"
  fi
  classify_installation
  if [[ "${installation_action}" == "newer" && "${allow_downgrade}" == true ]]; then
    installation_action="downgrade"
  elif [[ "${installation_action}" == "current" && "${force_install}" == true ]]; then
    installation_action="replace"
  fi
  print_release_summary "${architecture}"
  case "${installation_action}" in
    current)
      if [[ "${requested_version}" == "latest" ]]; then result "当前已是最新正式版本，无需更新。"; else result "当前已是指定版本，无需安装。"; fi
      return
      ;;
    newer) result "当前版本高于目标版本，已跳过以避免降级；需要降级时使用 --allow-downgrade。"; return ;;
    upgrade) info "准备从 ${current_version} 更新到 ${resolved_version}" ;;
    downgrade) warn "按显式参数从 ${current_version} 降级到 ${resolved_version}" ;;
    replace) info "准备重新安装 ${resolved_version}" ;;
  esac
  step "读取并校验发布文件"
  fetch "${release_base}/SHA256SUMS" "${temporary_dir}/SHA256SUMS" false "${max_release_api_size}" || die "下载校验清单失败"
  select_asset "${temporary_dir}/SHA256SUMS" "${architecture}"
  info "正在下载：${selected_asset}"
  fetch "${release_base}/${selected_asset}" "${temporary_dir}/${selected_asset}" true "${max_binary_size}" || die "下载程序失败"
  printf '%s  %s\n' "${selected_checksum}" "${selected_asset}" |
    (cd "${temporary_dir}" && sha256sum --check --status -) || die "SHA-256 校验失败"
  result "SHA-256 校验通过"
  chmod 0755 "${temporary_dir}/${selected_asset}" || die "无法设置候选程序权限"
  verify_binary "${temporary_dir}/${selected_asset}" || die "下载的程序无法运行或版本不匹配，原程序已保留"
  step "写入 ProxyDock"
  staged_path=$(mktemp "${install_dir}/.proxydock.new.XXXXXX") || die "无法创建安装候选文件"
  install -m 0755 "${temporary_dir}/${selected_asset}" "${staged_path}" || die "无法写入安装候选文件"
  if [[ -e "${install_path}" ]]; then
    backup_path=$(mktemp "${install_dir}/.proxydock.old.XXXXXX") || die "无法创建原程序备份"
    cp -p -- "${install_path}" "${backup_path}" || die "无法备份原程序"
  fi
  replacement_pending=true
  mv -f -- "${staged_path}" "${install_path}" || die "无法原子替换程序，正在恢复原程序"
  staged_path=""
  verify_binary "${install_path}" || die "安装后验证失败，正在恢复原程序"
  replacement_pending=false
  "${install_path}" --version
  print_completion
  info "运行 sudo ${install_path} 管理 Docker 代理；Xray 镜像升级使用 proxydock upgrade。"
}

if [[ "${BASH_SOURCE[0]:-$0}" == "$0" ]]; then
  main "$@"
fi
