#!/usr/bin/env bash
set -euo pipefail
image_name=${1:?镜像名称}
platform=${2:?目标架构}
case "$platform" in linux/amd64|linux/arm64) ;; *) exit 2 ;; esac
docker buildx build --platform "$platform" --file benzhi.Dockerfile --tag "${image_name}:latest" --load .
