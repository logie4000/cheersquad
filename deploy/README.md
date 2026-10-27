# Deploying CheerSquad

CheerSquad runs as one Docker container on the EC2 host (Amazon Linux 2023,
x86_64), next to the other sites. The Go server inside serves the API at
`/api/` and the web UI at `/`. Docker publishes it on `127.0.0.1:8085`
only, and the host Nginx proxies `https://www.teapothill.org/cheersquad/`
to it, stripping the `/cheersquad` prefix. The UI uses a relative base href and
relative API URLs, so it works under that path (or at the root locally).

## First deploy

```sh
# 1. Code lives under /srv like the other apps.
sudo git clone https://github.com/logie4000/CheerSquad.git /srv/cheersquad
cd /srv/cheersquad

# 2. Build and start the container.
sudo docker compose up -d --build
curl -s http://127.0.0.1:8085/api/healthz    # -> ok

# 3. Add the /cheersquad/ location to the existing www.teapothill.org
#    server block (paste deploy/nginx/cheersquad.location.conf into it, or
#    `include /srv/cheersquad/deploy/nginx/cheersquad.location.conf;`).
sudo nginx -t && sudo systemctl reload nginx
curl -s https://www.teapothill.org/cheersquad/api/healthz   # -> ok
```

HTTPS is already handled by the existing www.teapothill.org server block.

## Updating

```sh
cd /srv/cheersquad
sudo git pull
sudo docker compose up -d --build
sudo docker image prune -f
```

## Logs and status

```sh
sudo docker compose -f /srv/cheersquad/docker-compose.yml logs -f
sudo docker inspect --format '{{.State.Health.Status}}' cheersquad
```

If port 8085 is already taken by another site, change the left-hand side of
`ports` in `docker-compose.yml` and the `proxy_pass` port in the Nginx
snippet to match.
