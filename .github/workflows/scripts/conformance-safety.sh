#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 budget-state <spend> <cap> | validate-do-delete-status <status> | validate-do-page-url <url> <seen-file>" >&2
  exit 64
}

is_nonnegative_number() {
  [[ "$1" =~ ^[0-9]+([.][0-9]+)?([eE][+-]?[0-9]+)?$ ]]
}

case "${1:-}" in
  budget-state)
    [[ $# -eq 3 ]] || usage
    spend="$2"
    cap="$3"
    if ! is_nonnegative_number "${spend}" || ! is_nonnegative_number "${cap}"; then
      usage
    fi
    if awk -v spend="${spend}" -v cap="${cap}" 'BEGIN { exit !(spend >= cap) }'; then
      echo "at-or-over"
    else
      echo "below"
    fi
    ;;

  validate-do-delete-status)
    [[ $# -eq 2 ]] || usage
    status="$2"
    [[ "${status}" =~ ^[0-9]{3}$ ]] || usage
    if [[ "${status}" != "204" ]]; then
      echo "unexpected DigitalOcean DELETE status: ${status}; want 204" >&2
      exit 66
    fi
    ;;

  validate-do-page-url)
    [[ $# -eq 3 ]] || usage
    url="$2"
    seen_file="$3"
    [[ -n "${seen_file}" ]] || usage
    [[ "${url}" != *$'\n'* && "${url}" != *$'\r'* && "${url}" != *' '* && "${url}" != *$'\t'* ]] || exit 64

    without_scheme="${url#https://}"
    [[ "${without_scheme}" != "${url}" ]] || exit 64
    authority="${without_scheme%%/*}"
    [[ "${authority}" == "api.digitalocean.com" ]] || exit 64
    [[ "${without_scheme}" == */* ]] || exit 64

    touch "${seen_file}"
    if grep -Fqx -- "${url}" "${seen_file}"; then
      echo "repeated DigitalOcean pagination URL rejected: ${url}" >&2
      exit 65
    fi
    printf '%s\n' "${url}" >> "${seen_file}"
    ;;

  *)
    usage
    ;;
esac
