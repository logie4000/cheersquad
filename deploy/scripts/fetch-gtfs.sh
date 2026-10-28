#!/usr/bin/env bash
# Download GTFS schedule feeds for subway routing (run on the host from the
# repo checkout, /srv/cheersquad):
#
#   deploy/scripts/fetch-gtfs.sh nyc                 # one known feed
#   deploy/scripts/fetch-gtfs.sh nyc boston chicago  # several
#   deploy/scripts/fetch-gtfs.sh myfeed=https://example.org/gtfs.zip
#   GTFS_DIR=/tmp/gtfs deploy/scripts/fetch-gtfs.sh nyc
#
# Each feed is saved as <name>.zip in GTFS_DIR (default /srv/cheersquad/gtfs,
# which docker-compose.yml mounts at /data/gtfs). Downloads go to a temp file
# first and are moved into place only when complete, so a failed download
# never replaces a working feed. Restart the container afterwards
# (`docker restart cheersquad`) so the server reloads the feeds.
#
# Known feeds (the server uses their subway, metro and light rail lines, and
# rail lines that run at least every 15 minutes; never buses):
#   nyc      MTA New York City Transit subway
#   boston   MBTA (whole system; the T's subway and Green Line are used)
#   chicago  CTA (the 'L' lines are used)
#   dc       WMATA Metrorail; needs a free API key from
#            https://developer.wmata.com in WMATA_API_KEY
set -euo pipefail

dir="${GTFS_DIR:-/srv/cheersquad/gtfs}"

usage() {
  sed -n '2,22p' "$0" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

# fetch NAME URL [curl args...]
fetch() {
  local name=$1 url=$2
  shift 2
  local dest="$dir/$name.zip"
  local tmp
  tmp=$(mktemp "$dir/.$name.zip.XXXXXX")
  echo "fetching $name from $url"
  if ! curl -fsSL --retry 3 -o "$tmp" "$@" "$url"; then
    rm -f "$tmp"
    echo "failed to download $name" >&2
    return 1
  fi
  # A GTFS feed is a zip archive; catch HTML error pages served with 200.
  if [ "$(head -c 2 "$tmp")" != "PK" ]; then
    rm -f "$tmp"
    echo "$name: $url did not return a zip file" >&2
    return 1
  fi
  chmod 644 "$tmp"
  mv -f "$tmp" "$dest"
  echo "saved $dest ($(wc -c <"$dest") bytes)"
}

[ $# -gt 0 ] || usage 1
case "$1" in -h | --help) usage ;; esac

mkdir -p "$dir"
status=0
for arg in "$@"; do
  case "$arg" in
    nyc) fetch nyc http://web.mta.info/developers/data/nyct/subway/google_transit.zip || status=1 ;;
    boston) fetch boston https://cdn.mbta.com/MBTA_GTFS.zip || status=1 ;;
    chicago) fetch chicago https://www.transitchicago.com/downloads/sch_data/google_transit.zip || status=1 ;;
    dc)
      if [ -z "${WMATA_API_KEY:-}" ]; then
        echo "dc: set WMATA_API_KEY (free key from https://developer.wmata.com)" >&2
        status=1
        continue
      fi
      # Header from a file descriptor keeps the key out of the process list.
      fetch dc https://api.wmata.com/gtfs/rail-gtfs-static.zip \
        -H @<(printf 'api_key: %s\n' "$WMATA_API_KEY") || status=1
      ;;
    *=http://* | *=https://*)
      name=${arg%%=*}
      if ! [[ $name =~ ^[A-Za-z0-9_-]+$ ]]; then
        echo "bad feed name '$name' (use letters, digits, - and _)" >&2
        status=1
        continue
      fi
      fetch "$name" "${arg#*=}" || status=1
      ;;
    http://* | https://*)
      # A bare URL is saved under a name derived from its host.
      host=${arg#*://}
      host=${host%%/*}
      fetch "${host//[^A-Za-z0-9_-]/_}" "$arg" || status=1
      ;;
    *)
      echo "unknown feed '$arg'; known: nyc boston chicago dc, or NAME=URL" >&2
      status=1
      ;;
  esac
done
exit $status
