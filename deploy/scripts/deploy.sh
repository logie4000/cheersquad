#!/usr/bin/env bash
# Deploy on the EC2 host (run from the repo checkout, /srv/cheersquad):
#
#   IMAGE_TAG=<git sha> deploy/scripts/deploy.sh
#
# The tag is a full commit SHA from main that the Images workflow built (its
# run summary lists them), or omit IMAGE_TAG for the newest build ("latest").
#
# CheerSquad has no secrets, so unlike sweat-equity there is nothing in SSM:
# the region comes from the instance metadata (or AWS_REGION) and the
# registry from the instance role's account. Both are saved to .env so plain
# `docker compose ... --env-file .env` commands work afterwards.
set -euo pipefail
cd "$(dirname "$0")/../.."

if [ -z "${AWS_REGION:-}" ]; then
  token=$(curl -fsS -X PUT http://169.254.169.254/latest/api/token \
    -H 'X-aws-ec2-metadata-token-ttl-seconds: 60')
  AWS_REGION=$(curl -fsS -H "X-aws-ec2-metadata-token: $token" \
    http://169.254.169.254/latest/meta-data/placement/region)
fi
account=$(aws sts get-caller-identity --query Account --output text)
export AWS_REGION
export ECR_REGISTRY="${ECR_REGISTRY:-$account.dkr.ecr.$AWS_REGION.amazonaws.com}"
export COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-cheersquad}"
export IMAGE_TAG="${IMAGE_TAG:-latest}"

# Street routers: run one for each OSRM profile deploy/scripts/build-osrm.sh
# has built, and point the app at it.
osrm_profiles=() osrm_env=()
for p in foot:WALK bicycle:BIKE car:DRIVE; do
  name=${p%:*}
  if [ -e "osrm/$name/area.osrm.mldgr" ]; then
    osrm_profiles+=("osrm-$name")
    osrm_env+=("OSRM_${p#*:}_URL=http://osrm-$name:5000")
  fi
done
cat > .env <<ENV
AWS_REGION=$AWS_REGION
ECR_REGISTRY=$ECR_REGISTRY
COMPOSE_PROJECT_NAME=$COMPOSE_PROJECT_NAME
IMAGE_TAG=$IMAGE_TAG
COMPOSE_PROFILES=$(IFS=,; echo "${osrm_profiles[*]}")
$(printf '%s\n' "${osrm_env[@]}")
ENV
[ ${#osrm_profiles[@]} -eq 0 ] || echo "street routing: ${osrm_profiles[*]}"
echo "deploying $COMPOSE_PROJECT_NAME-app:$IMAGE_TAG"

aws ecr get-login-password --region "$AWS_REGION" | docker login --username AWS --password-stdin "$ECR_REGISTRY"

dc=(docker compose -f docker-compose.yml -f deploy/docker-compose.prod.yml --env-file .env)
"${dc[@]}" pull
"${dc[@]}" up -d --remove-orphans

# Wait for the container's health check so a bad image fails the deploy.
for _ in $(seq 1 30); do
  status=$(docker inspect --format '{{.State.Health.Status}}' cheersquad 2>/dev/null || echo missing)
  [ "$status" = healthy ] && break
  sleep 2
done
"${dc[@]}" ps
[ "$status" = healthy ] || { echo "cheersquad is $status after 60s; see: docker logs cheersquad" >&2; exit 1; }
docker image prune -f >/dev/null
echo "cheersquad $IMAGE_TAG is up"
