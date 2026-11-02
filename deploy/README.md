# Deploying CheerSquad

CheerSquad runs as one Docker container on the EC2 host (Denali: Amazon
Linux 2023, x86_64), next to the other apps. The Go server inside serves the
API at `/api/` and the web UI at `/`. Docker publishes it on
`127.0.0.1:8085` only, and the host Nginx proxies
`https://www.specialbrew.net/cheersquad/` to it, stripping the `/cheersquad`
prefix. The UI uses a relative base href and relative API URLs, so it works
under that path (or at the root locally).

Images are built by GitHub, not on the host, the same way sweat-equity does
it: after CI passes on `main`, the **Images** workflow builds the image and
pushes it to ECR as `cheersquad-app:<sha>` and `:latest`. On the host,
`deploy/scripts/deploy.sh` pulls an image and restarts the container.

## One-time AWS setup

These mirror sweat-equity's steps (its `docs/deploy-denali.md`) with
`cheersquad` names.

1. **ECR push role for GitHub.** Create a role `cheersquad-github` with the
   same trust policy as `sweat-equity-github`, but with `sub` set to this
   repository's main branch, e.g.
   `repo:logie4000@34590917/cheersquad@1436165275:ref:refs/heads/main`.
   To check the exact prefix, run
   `gh api repos/logie4000/cheersquad/actions/oidc/customization/sub`.
   Give it the same inline ECR policy, with `Resource`
   `arn:aws:ecr:<region>:<account>:repository/cheersquad-*`.
2. **Repository variables** (GitHub, Settings > Secrets and variables >
   Actions > Variables): `AWS_ROLE_ARN` (that role), `AWS_REGION`, and
   `COMPOSE_PROJECT_NAME=cheersquad`. After the next green CI run on `main`,
   the Images workflow pushes `cheersquad-app` (or start it from Actions >
   Images > Run workflow). It creates the ECR repository on first use.
3. **Instance role** (Denali's): make sure its ECR pull policy also covers
   `repository/cheersquad-*` (skip this if it isn't scoped to other apps'
   prefixes). CheerSquad has no secrets, so it needs nothing from SSM.

## First deploy

```sh
# 1. Code lives under /srv like the other apps. The checkout is only for
#    the compose files and deploy script; the image comes from ECR.
sudo git clone https://github.com/logie4000/cheersquad.git /srv/cheersquad
sudo chown -R "$USER": /srv/cheersquad     # deploy.sh writes .env here
cd /srv/cheersquad

# 2. Pull the newest image and start it (waits for the health check).
deploy/scripts/deploy.sh
curl -s http://127.0.0.1:8085/api/healthz    # -> ok

# 3. Add the /cheersquad/ locations to the existing www.specialbrew.net
#    server block (paste deploy/nginx/cheersquad.location.conf into it, or
#    `include /srv/cheersquad/deploy/nginx/cheersquad.location.conf;`), and
#    the plan rate-limit zone to the http context.
sudo cp deploy/nginx/cheersquad.http.conf /etc/nginx/conf.d/
sudo nginx -t && sudo systemctl reload nginx
curl -s https://www.specialbrew.net/cheersquad/api/healthz   # -> ok
```

HTTPS is already handled by the existing www.specialbrew.net server block.

## Updating and rolling back

```sh
cd /srv/cheersquad
git pull                                  # only needed if deploy files changed
deploy/scripts/deploy.sh                  # newest build (latest)
IMAGE_TAG=<full sha> deploy/scripts/deploy.sh   # a specific build, e.g. to roll back
```

The SHAs are listed in each Images run's summary.

## Subway routing

In transit mode the planner routes over real subway lines from GTFS
schedule feeds (the zip files transit agencies publish). Only subway, metro
and light rail lines are used (light rail covers lines like Boston's Green
Line), plus rail lines such as Berlin's S-Bahn or Paris's RER when they run
at least every 15 minutes on Sunday morning (hourly commuter rail is left
out). Buses never are: on marathon day they are rerouted or stuck behind
closures, so they aren't a realistic option. Waits come from each line's
scheduled frequency, not exact departures.

The feeds live in `/srv/cheersquad/gtfs`, which `docker-compose.yml` mounts
read-only at `/data/gtfs` (`GTFS_DIR`). They aren't committed or baked into
the image; fetch them on the host:

```sh
cd /srv/cheersquad
deploy/scripts/fetch-gtfs.sh nyc            # see fetch-gtfs.sh --help for all cities, or NAME=URL
docker restart cheersquad                   # the server loads feeds at startup
docker logs cheersquad 2>&1 | grep -i gtfs  # check which feeds loaded
```

`dc` (WMATA Metrorail) needs a free API key from
<https://developer.wmata.com>: `WMATA_API_KEY=... deploy/scripts/fetch-gtfs.sh dc`.
`london` builds a feed from TfL's timetables, which TfL publishes as
TransXChange rather than GTFS: it downloads them, keeps the Underground, DLR
and tram files (TfL's timetables leave out the Overground and Elizabeth
line), and converts them with
[transx2gtfs](https://github.com/HTenkanen/transx2gtfs) inside a
`python:3.12-slim` container (`deploy/scripts/tfl_to_gtfs.py`), so it needs
docker but no Python on the host. The timetables are public, so no TfL key
is needed. The conversion
also downloads the NaPTAN stop list (about 100 MB) and takes a while.

TfL's firewall answers 403 to some cloud hosts, denali included. Download
<https://tfl.gov.uk/cdn/static/cms/documents/journey-planner-timetables.zip>
in a browser instead, copy it to the host (anywhere but the GTFS folder,
which the server reads every zip in), and point the script at it:

```sh
TFL_TIMETABLES_FILE=~/journey-planner-timetables.zip deploy/scripts/fetch-gtfs.sh london
```

On Windows, from the repo folder in PowerShell, with the zip in Downloads:

```powershell
docker run --rm -v "${PWD}/gtfs:/gtfs" -v "${PWD}/deploy/scripts:/scripts:ro" `
  -v "${HOME}/Downloads:/tfl:ro" -e TFL_TIMETABLES_FILE=/tfl/journey-planner-timetables.zip `
  python:3.12-slim sh -c `
  "pip install -q --root-user-action=ignore transx2gtfs==0.6.0 && python /scripts/tfl_to_gtfs.py /gtfs/london.zip"
```

Also built in: `philadelphia` (SEPTA), `minneapolis` (Metro Transit),
`paris` (Ile-de-France Mobilites, about 145 MB), `rome` (Roma Mobilita), `sf`
and `sydney`. A city whose agencies publish separately gets a folder of
zips, which the server merges into one network with walking transfers
between them: `sf/` holds BART and Muni, `sydney/` holds Sydney Trains,
Metro and light rail. `sydney` needs a free Transport for NSW key from
<https://opendata.transport.nsw.gov.au>:
`TFNSW_API_KEY=... deploy/scripts/fetch-gtfs.sh sydney`. Cape Town has no
subway or metro (MyCiTi is bus rapid transit), so it has no feed.

Any other agency's feed works with `NAME=URL`; to merge several agencies,
save their zips in one folder under the GTFS folder. Agencies publish new feeds a
few times a year (service changes, holidays), so refetch before a race and
restart; a failed download leaves the old file in place. With no feeds, or
a course outside every feed's area, transit falls back to the straight-line
estimate, and the plan says which routing it used.

Walking legs are straight-line estimates by default. For street-network
walking times, run a self-hosted [OSRM](https://project-osrm.org/) with the
foot profile and uncomment `OSRM_WALK_URL` in `docker-compose.yml`. It's
optional.

## Logs and status

```sh
docker logs -f cheersquad
docker inspect --format '{{.State.Health.Status}}' cheersquad
```

## Building on the host instead

`docker compose up -d --build` builds locally from `docker-compose.yml`
alone. On Amazon Linux 2023 that needs buildx 0.17 or later, which the
`docker` package doesn't ship; `docker build -t cheersquad:latest . &&
docker compose up -d` works without it.

If port 8085 is already taken by another app, change the left-hand side of
`ports` in `docker-compose.yml` and the `proxy_pass` port in the Nginx
snippet to match.
