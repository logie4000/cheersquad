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
   prefixes). For street routes it also needs `ssm:GetParameter` on
   `arn:aws:ssm:<region>:<account>:parameter/webapp/cheer-squad/*` (and
   `kms:Decrypt` if the parameter uses a customer-managed key; the default
   `aws/ssm` key needs nothing extra).
4. **OpenRouteService key** (optional, for street routes): sign up at
   [openrouteservice.org](https://openrouteservice.org/dev/#/signup), copy
   the free key, and store it once:
   `aws ssm put-parameter --name /webapp/cheer-squad/ORS_API_KEY --type SecureString --value '<key>'`.
   Every deploy reads it into `.env`. Without it plans use straight-line
   estimates.

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
<https://developer.wmata.com>. Store it once in SSM and the script reads it
(the instance role's `ssm:GetParameter` on `/webapp/cheer-squad/*` covers it):
`aws ssm put-parameter --name /webapp/cheer-squad/WMATA_API_KEY --type SecureString --value '<key>'`,
then `deploy/scripts/fetch-gtfs.sh dc`. `WMATA_API_KEY=...` in the
environment still works and wins.
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
`paris` (Ile-de-France Mobilites, about 145 MB), `rome` (Roma Mobilita), `la`
(LA Metro Rail), `sf`
and `sydney`. A city whose agencies publish separately gets a folder of
zips, which the server merges into one network with walking transfers
between them: `sf/` holds BART and Muni, `sydney/` holds Sydney Trains,
Metro and light rail. `sydney` needs a free Transport for NSW key from
<https://opendata.transport.nsw.gov.au>, stored in SSM like WMATA's:
`aws ssm put-parameter --name /webapp/cheer-squad/TFNSW_API_KEY --type SecureString --value '<key>'`,
then `deploy/scripts/fetch-gtfs.sh sydney` (`TFNSW_API_KEY=...` in the
environment wins). Cape Town has no
subway or metro (MyCiTi is bus rapid transit), so it has no feed.

Any other agency's feed works with `NAME=URL`; to merge several agencies,
save their zips in one folder under the GTFS folder. Agencies publish new feeds a
few times a year (service changes, holidays), so refetch before a race and
restart; a failed download leaves the old file in place. With no feeds, or
a course outside every feed's area, transit mode walks between spots instead
of guessing at transit speeds, and the sightings table says no transit data
is available.

With the OpenRouteService key in SSM (see above), walking, biking and
driving plans first time every leg the plan could use with ORS's matrix
API (typically 3-4 requests), are solved on those street times, and then
fetch street directions for just the chosen legs (one request per stop),
which are drawn on the map, in Print and in the PDF. Transit plans (walks
to stations) and plans that avoid ferries, which the matrix can't do,
street route the chosen legs and re-plan instead (up to 6 rounds). The
free plan allows 500 matrix and 2,000 directions requests a day and 40 a
minute of each; the server stays under those (450 and 1,900 a day) and
falls back to straight-line estimates past them or while ORS is down
(the routing line under the plan says so). Answers are saved in the
`ors-cache` Docker volume (`/data/cache/ors.jsonl`, `ORS_CACHE_FILE`), so
they outlive restarts and deploys: once a course has been planned, other
plans of it (other paces, modes already used) cost no requests, and a new
starting point costs one matrix request plus directions for legs not seen
before. Saved answers are kept for 180 days. The file grows to roughly
40 MB at most; to start over, `docker compose down` and
`docker volume rm cheersquad_ors-cache`. Use a separate ORS key for local
testing so it doesn't spend prod's quota.

For street-network walking times while planning too, run a self-hosted
[OSRM](https://project-osrm.org/) with the foot profile and uncomment
`OSRM_WALK_URL` in `docker-compose.yml`. It's optional.

## Search engines

The server fills in the page's canonical URL, Open Graph card and
structured data from the request (Host, `X-Forwarded-Proto`,
`X-Forwarded-Prefix`, all set by the Nginx snippet). Each preloaded race
has its own page at `/cheersquad/race/<id>` (its own title, description and
intro; the app opens with that race loaded), and `/cheersquad/sitemap.xml`
lists the front page and every race page. Set `SITE_URL=https://<host>/cheersquad/` in the
container's environment to pin it instead. API answers carry
`X-Robots-Tag: noindex`.

Crawlers only read `robots.txt` at the domain root, which this app doesn't
own. Add this line to the root site's `robots.txt` (creating it if there
is none):

```text
Sitemap: https://<host>/cheersquad/sitemap.xml
```

Don't disallow `/cheersquad/api/`: Google renders the page with JavaScript
and needs `api/races` to do it. The `noindex` header already keeps the API
itself out of results.

Then add the site in Google Search Console (and Bing Webmaster Tools),
submit the sitemap, and use URL Inspection on `https://<host>/cheersquad/`
to request indexing.

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
