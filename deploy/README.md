# Deploying CheerSquad

CheerSquad runs as one Docker container on the EC2 host (Denali: Amazon
Linux 2023, x86_64), next to the other apps. The Go server inside serves the
API at `/api/` and the web UI at `/`. Docker publishes it on
`127.0.0.1:8085` only, and the host Nginx proxies
`https://www.teapothill.org/cheersquad/` to it, stripping the `/cheersquad`
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

# 3. Add the /cheersquad/ location to the existing www.teapothill.org
#    server block (paste deploy/nginx/cheersquad.location.conf into it, or
#    `include /srv/cheersquad/deploy/nginx/cheersquad.location.conf;`).
sudo nginx -t && sudo systemctl reload nginx
curl -s https://www.teapothill.org/cheersquad/api/healthz   # -> ok
```

HTTPS is already handled by the existing www.teapothill.org server block.

## Updating and rolling back

```sh
cd /srv/cheersquad
git pull                                  # only needed if deploy files changed
deploy/scripts/deploy.sh                  # newest build (latest)
IMAGE_TAG=<full sha> deploy/scripts/deploy.sh   # a specific build, e.g. to roll back
```

The SHAs are listed in each Images run's summary.

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
