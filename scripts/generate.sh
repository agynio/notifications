#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
PROTO_DIR="${ROOT_DIR}/internal/.proto"
GEN_DIR="${ROOT_DIR}/internal/.gen"

export PATH="$(go env GOPATH)/bin:${PATH}"

rm -rf "${PROTO_DIR}" "${GEN_DIR}"
mkdir -p "${PROTO_DIR}" "${GEN_DIR}"

buf export buf.build/agynio/api --output "${PROTO_DIR}"
buf generate "${PROTO_DIR}" --template "${ROOT_DIR}/buf.gen.yaml"
