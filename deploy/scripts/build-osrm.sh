#!/usr/bin/env bash
# Build street-routing data for walking, biking and driving (run on the host
# from the repo checkout, /srv/cheersquad, or on any machine with docker and
# copy the folder up):
#
#   deploy/scripts/build-osrm.sh                  # every preloaded race city
#   deploy/scripts/build-osrm.sh boston nyc       # add or refresh some
#   deploy/scripts/build-osrm.sh paris=https://download.geofabrik.de/europe/france/ile-de-france-latest.osm.pbf@2.20,48.80,2.45,48.92
#   OSRM_DIR=/tmp/osrm deploy/scripts/build-osrm.sh berlin
#
# Each city is an OpenStreetMap extract from Geofabrik, clipped to a box
# around its marathon course (about 5 km of margin), so the build stays small.
# NAME=URL@WEST,SOUTH,EAST,NORTH adds any other city. Clipped areas are kept
# in OSRM_DIR/areas, so later runs add to the cities already built; delete a
# file there to drop a city. All areas are merged and built once per OSRM
# profile (foot, bicycle, car) into OSRM_DIR/<profile>, which the osrm-*
# services in docker-compose.yml serve. A profile is swapped into place only
# when its build succeeds, and a router already running is restarted on it.
# The first time, run deploy/scripts/deploy.sh afterwards: it starts the
# routers for whichever profiles are built and points the app at them.
#
# The build uses every core for minutes per profile; to keep that off a
# busy host, build elsewhere and copy the folders (deploy/README.md).
#
# Needs docker. Downloads are cached in OSRM_DIR/src (the US state and Kanto
# extracts are a few hundred MB each); delete it when done to free the space.
# OSRM_PROFILES limits the profiles built, e.g. OSRM_PROFILES="foot car".
#
# Known cities:
#   berlin boston chicago london nyc tokyo
set -euo pipefail

dir="${OSRM_DIR:-/srv/cheersquad/osrm}"
image="${OSRM_IMAGE:-ghcr.io/project-osrm/osrm-backend:v5.27.1}"
osmium_image="${OSMIUM_IMAGE:-debian:bookworm-slim}"
profiles="${OSRM_PROFILES:-foot bicycle car}"
geofabrik=https://download.geofabrik.de
profiles_dir="$(cd "$(dirname "$0")/../osrm" && pwd)"
all=(berlin boston chicago london nyc tokyo)

usage() {
  sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

# city NAME: prints "URL BBOX" for a known city. The boxes are each course's
# bounds plus about 5 km.
city() {
  case "$1" in
    berlin) echo "$geofabrik/europe/germany/berlin-latest.osm.pbf 13.20,52.42,13.51,52.58" ;;
    boston) echo "$geofabrik/north-america/us/massachusetts-latest.osm.pbf -71.58,42.18,-71.01,42.40" ;;
    chicago) echo "$geofabrik/north-america/us/illinois-latest.osm.pbf -87.74,41.78,-87.56,42.00" ;;
    london) echo "$geofabrik/europe/united-kingdom/england/greater-london-latest.osm.pbf -0.22,51.42,0.14,51.56" ;;
    nyc) echo "$geofabrik/north-america/us/new-york-latest.osm.pbf -74.12,40.55,-73.86,40.86" ;;
    tokyo) echo "$geofabrik/asia/japan/kanto-latest.osm.pbf 139.63,35.60,139.86,35.76" ;;
    *) return 1 ;;
  esac
}

# download URL: saves the extract to src/ unless it is already there.
download() {
  local url=$1 file
  file="$dir/src/$(basename "$url")"
  if [ -s "$file" ] && [ -z "${REFRESH:-}" ]; then
    echo "using cached $file (REFRESH=1 to download again)"
    return
  fi
  local tmp
  tmp=$(mktemp "$file.XXXXXX")
  echo "downloading $url"
  if ! curl -fSL --retry 3 -o "$tmp" "$url"; then
    rm -f "$tmp"
    echo "failed to download $url" >&2
    return 1
  fi
  mv -f "$tmp" "$file"
}

[ "${1:-}" = -h ] || [ "${1:-}" = --help ] && usage
[ $# -gt 0 ] || set -- "${all[@]}"

mkdir -p "$dir/src" "$dir/areas"
clip=()
for arg in "$@"; do
  case "$arg" in
    *=*@*)
      name=${arg%%=*}
      rest=${arg#*=}
      url=${rest%@*}
      bbox=${rest##*@}
      ;;
    *)
      if ! spec=$(city "$arg"); then
        echo "unknown city $arg (known: ${all[*]}; or NAME=URL@WEST,SOUTH,EAST,NORTH)" >&2
        exit 1
      fi
      name=$arg
      url=${spec% *}
      bbox=${spec#* }
      ;;
  esac
  case "$name" in *[!a-z0-9_-]* | "") echo "bad city name $name" >&2 && exit 1 ;; esac
  download "$url"
  clip+=("osmium extract --overwrite -b $bbox -o /data/areas/$name.osm.pbf /data/src/$(basename "$url")")
done

# Clip and merge with osmium (from Debian's package, so no third-party image).
echo "clipping ${*} and merging with the areas already built"
docker run --rm -v "$dir:/data" "$osmium_image" sh -ec "
  apt-get update -qq >/dev/null && apt-get install -y -qq osmium-tool >/dev/null
  $(printf '%s\n' "${clip[@]}")
  osmium merge --overwrite -o /data/area.osm.pbf /data/areas/*.osm.pbf
  chown $(id -u):$(id -g) /data/area.osm.pbf /data/areas/*.osm.pbf
"
echo "areas: $(cd "$dir/areas" && ls | sed 's/\.osm\.pbf$//' | tr '\n' ' ')"

for p in $profiles; do
  work="$dir/.build-$p"
  rm -rf "$work"
  mkdir -p "$work"
  ln "$dir/area.osm.pbf" "$work/area.osm.pbf" 2>/dev/null || cp "$dir/area.osm.pbf" "$work/area.osm.pbf"
  echo "building the $p profile"
  # Foot and bicycle go through a wrapper that lets plans leave ferries out
  # (the stock car profile already can).
  lua=/opt/$p.lua
  [ "$p" = car ] || lua=/profiles/excludable-ferry.lua
  docker run --rm --user "$(id -u):$(id -g)" -e OSRM_BASE_PROFILE="$p" \
    -v "$profiles_dir:/profiles:ro" -v "$work:/data" "$image" sh -ec "
    osrm-extract -p $lua /data/area.osm.pbf
    osrm-partition /data/area.osrm
    osrm-customize /data/area.osrm
  "
  rm -f "$work/area.osm.pbf"
  rm -rf "$dir/$p.old"
  [ -d "$dir/$p" ] && mv "$dir/$p" "$dir/$p.old"
  mv "$work" "$dir/$p"
  rm -rf "$dir/$p.old"
  echo "built $dir/$p ($(du -sh "$dir/$p" | cut -f1))"
done
rm -f "$dir/area.osm.pbf"

# A running router keeps serving the files it opened, so restart it on the
# new data (when building on another machine there is none to restart).
for p in $profiles; do
  c=cheersquad-osrm-$p
  if [ "$(docker inspect --format '{{.State.Running}}' "$c" 2>/dev/null)" = true ]; then
    echo "restarting $c"
    docker restart "$c" >/dev/null
  fi
done
echo "done; the first time, run deploy/scripts/deploy.sh to start the routers"
