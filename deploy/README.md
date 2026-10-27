# Deploying CheerSquad

CheerSquad runs as one Docker container on the EC2 host (Amazon Linux 2023,
x86_64), next to the other sites. The Go server inside serves the API at
`/api/` and the web UI at `/`. Docker publishes it on `127.0.0.1:8085`
only, and the host Nginx proxies `www.teapothill.org` to it.

## First deploy

```sh
# 1. Code lives under /srv like the other apps.
sudo git clone https://github.com/logie4000/CheerSquad.git /srv/cheersquad
cd /srv/cheersquad

# 2. Build and start the container.
sudo docker compose up -d --build
curl -s http://127.0.0.1:8085/api/healthz    # -> ok

# 3. Add the Nginx site and reload.
sudo cp deploy/nginx/www.teapothill.org.conf /etc/nginx/conf.d/
sudo nginx -t && sudo systemctl reload nginx

# 4. HTTPS (once DNS for www.teapothill.org points at this instance).
sudo certbot --nginx -d www.teapothill.org
```

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
`ports` in `docker-compose.yml` and the `proxy_pass` port in the Nginx file
to match.
