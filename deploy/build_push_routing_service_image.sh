#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
AWS_REGION="${AWS_REGION:-us-west-2}"
AWS_ACCOUNT_ID="${AWS_ACCOUNT_ID:-507254053937}"
ECR_REPO_NAME="${ECR_REPO_NAME:-finalroundai/sub2api-openai-routing-service}"
ECR_REGISTRY="${AWS_ACCOUNT_ID}.dkr.ecr.${AWS_REGION}.amazonaws.com"
IMAGE_URI="${ECR_REGISTRY}/${ECR_REPO_NAME}"
GIT_HASH="${GIT_HASH:-$(git -C "${REPO_ROOT}" rev-parse --short HEAD)}"
IMAGE_TAG="${IMAGE_TAG:-${GIT_HASH}}"
PLATFORM="${PLATFORM:-linux/amd64}"
LIFECYCLE_FILE="${REPO_ROOT}/deploy/ecr-lifecycle-routing-service.json"

echo "[routing-image] repo=${IMAGE_URI}"
echo "[routing-image] tag=${IMAGE_TAG}"
echo "[routing-image] platform=${PLATFORM}"

aws ecr describe-repositories \
  --repository-names "${ECR_REPO_NAME}" \
  --region "${AWS_REGION}" >/dev/null 2>&1 || \
aws ecr create-repository \
  --repository-name "${ECR_REPO_NAME}" \
  --image-scanning-configuration scanOnPush=true \
  --region "${AWS_REGION}" >/dev/null

aws ecr put-lifecycle-policy \
  --repository-name "${ECR_REPO_NAME}" \
  --lifecycle-policy-text "file://${LIFECYCLE_FILE}" \
  --region "${AWS_REGION}" >/dev/null

aws ecr get-login-password --region "${AWS_REGION}" | \
  docker login --username AWS --password-stdin "${ECR_REGISTRY}" >/dev/null

docker buildx build \
  --platform "${PLATFORM}" \
  --file "${REPO_ROOT}/Dockerfile.routing-service" \
  --tag "${IMAGE_URI}:${IMAGE_TAG}" \
  --build-arg COMMIT="${GIT_HASH}" \
  --build-arg VERSION="${IMAGE_TAG}" \
  --push \
  "${REPO_ROOT}"

echo "[routing-image] pushed ${IMAGE_URI}:${IMAGE_TAG}"
