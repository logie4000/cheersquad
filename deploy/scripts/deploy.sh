#!/usr/bin/env bash
# Deploy on the EC2 host (run from the repo checkout, /srv/cheersquad):
#
#   IMAGE_TAG=<git sha> deploy/scripts/deploy.sh
#
# The tag is a full commit SHA from main that the Images workflow built (its
# run summary lists them), or omit IMAGE_TAG for the newest build ("latest").
#
# The region comes from the instance metadata (or AWS_REGION) and the
# registry from the instance role's account. The one secret, the
# OpenRouteService key for street routes, is read from SSM Parameter Store
# under /webapp/ like the other apps' (SecureString ORS_PARAM, default
# /webapp/cheersquad/ORS_API_KEY); without it plans use straight-line
# estimates. All of it is saved to .env
# (mode 600) so plain `docker compose ... --env-file .env` commands work
# afterwards.
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
ors_param="${ORS_PARAM:-/webapp/cheersquad/ORS_API_KEY}"
if ! ORS_API_KEY=$(aws ssm get-parameter --region "$AWS_REGION" --name "$ors_param" \
    --with-decryption --query Parameter.Value --output text 2>/dev/null); then
  ORS_API_KEY=""
  echo "no $ors_param in SSM (or no access): street routing off, legs are estimates" >&2
fi
umask 077
cat > .env <<ENV
AWS_REGION=$AWS_REGION
ECR_REGISTRY=$ECR_REGISTRY
COMPOSE_PROJECT_NAME=$COMPOSE_PROJECT_NAME
IMAGE_TAG=$IMAGE_TAG
ORS_API_KEY=$ORS_API_KEY
ENV
chmod 600 .env
umask 022
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
