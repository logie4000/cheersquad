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
#   london   TfL Underground, DLR and trams,
#            converted from TfL's TransXChange timetables in a python
#            container (needs docker; no TfL key needed). If TfL
#            refuses the download (403 from cloud hosts), download
#            journey-planner-timetables.zip in a browser and set
#            TFL_TIMETABLES_FILE to it
#   philadelphia  SEPTA (Market-Frankford, Broad Street, trolleys; one zip
#            holding SEPTA's bus and rail feeds, which the server merges)
#   minneapolis   Metro Transit (Blue and Green light rail lines)
#   paris    Ile-de-France Mobilites (metro, RER, trams; about 145 MB)
#   rome     Roma Mobilita (metro A, B, C and trams)
#   la       LA Metro Rail (B and D subway, A, C, E and K light rail)
#   sf       BART and Muni (Muni Metro), saved as sf/bart.zip and
#            sf/muni.zip; a folder of zips loads as one network
#   sydney   Sydney Trains, Metro and light rail, saved in sydney/; needs a
#            free API key from https://opendata.transport.nsw.gov.au in
#            TFNSW_API_KEY
# (Cape Town has no subway or metro: MyCiTi is bus rapid transit.)
set -euo pipefail

dir="${GTFS_DIR:-/srv/cheersquad/gtfs}"

usage() {
  sed -n '2,39p' "$0" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

# fetch NAME URL [curl args...]; NAME may be CITY/FEED, for one of several
# feeds in a city folder.
fetch() {
  local name=$1 url=$2
  shift 2
  local dest="$dir/$name.zip"
  local tmp
  mkdir -p "$(dirname "$dest")"
  tmp=$(mktemp "$(dirname "$dest")/.$(basename "$name").zip.XXXXXX")
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
    philadelphia) fetch philadelphia https://github.com/septadev/GTFS/releases/latest/download/gtfs_public.zip || status=1 ;;
    minneapolis) fetch minneapolis https://svc.metrotransit.org/mtgtfs/gtfs.zip || status=1 ;;
    paris) fetch paris https://eu.ftp.opendatasoft.com/stif/GTFS/IDFM-gtfs.zip || status=1 ;;
    rome) fetch rome https://romamobilita.it/sites/default/files/rome_static_gtfs.zip || status=1 ;;
    la) fetch la https://gitlab.com/LACMTA/gtfs_rail/-/raw/master/gtfs_rail.zip || status=1 ;;
    sf)
      fetch sf/bart https://www.bart.gov/dev/schedules/google_transit.zip || status=1
      fetch sf/muni https://muni-gtfs.apps.sfmta.com/data/muni_gtfs-current.zip || status=1
      ;;
    sydney)
      if [ -z "${TFNSW_API_KEY:-}" ]; then
        echo "sydney: set TFNSW_API_KEY (free key from https://opendata.transport.nsw.gov.au)" >&2
        status=1
        continue
      fi
      tfnsw=https://api.transport.nsw.gov.au
      for feed in trains=v1/gtfs/schedule/sydneytrains metro=v2/gtfs/schedule/metro \
        lightrail-cbd=v1/gtfs/schedule/lightrail/cbdandsoutheast \
        lightrail-innerwest=v1/gtfs/schedule/lightrail/innerwest; do
        fetch "sydney/${feed%%=*}" "$tfnsw/${feed#*=}" \
          -H @<(printf 'Authorization: apikey %s\n' "$TFNSW_API_KEY") || status=1
      done
      ;;
    capetown | cape-town)
      echo "$arg: Cape Town has no subway or metro (MyCiTi is bus rapid transit), so there is no feed to use" >&2
      status=1
      ;;
    london)
      # TfL publishes TransXChange, not GTFS; convert it with transx2gtfs.
      scripts=$(cd "$(dirname "$0")" && pwd)
      out=$(cd "$dir" && pwd)
      # TfL's firewall refuses some cloud hosts (denali gets 403), so
      # TFL_TIMETABLES_FILE can name a copy downloaded elsewhere.
      local_copy=()
      if [[ -n ${TFL_TIMETABLES_FILE:-} ]]; then
        if [[ ! -f $TFL_TIMETABLES_FILE ]]; then
          echo "TFL_TIMETABLES_FILE: no such file: $TFL_TIMETABLES_FILE" >&2
          exit 1
        fi
        tfl_dir=$(cd "$(dirname "$TFL_TIMETABLES_FILE")" && pwd)
        local_copy=(-v "$tfl_dir:/tfl:ro" -e "TFL_TIMETABLES_FILE=/tfl/$(basename "$TFL_TIMETABLES_FILE")")
        echo "converting london from $TFL_TIMETABLES_FILE (this takes a while)"
      else
        echo "fetching london from TfL and converting (this takes a while)"
      fi
      docker run --rm -v "$out:/gtfs" -v "$scripts:/scripts:ro" "${local_copy[@]}" \
        -e TFL_APP_KEY -e TFL_TIMETABLES_URL \
        python:3.12-slim sh -c \
        'pip install -q --root-user-action=ignore transx2gtfs==0.6.0 && python /scripts/tfl_to_gtfs.py /gtfs/london.zip' ||
        status=1
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
      echo "unknown feed '$arg'; known: nyc boston chicago dc london philadelphia minneapolis paris rome la sf sydney, or NAME=URL" >&2
      status=1
      ;;
  esac
done
exit $status
