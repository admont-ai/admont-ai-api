# ---- Build stage ----
FROM golang:1.25-bookworm AS build

RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates gcc libc6-dev && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -o /bin/admont-ai-api .

# ---- Model stage ----
# Fetch a prebuilt ONNX export of all-MiniLM-L6-v2 instead of exporting it at
# build time. The prebuilt graph exposes `last_hidden_state` with inputs
# input_ids/attention_mask/token_type_ids — exactly what the Go embedder
# consumes (it does its own mean pooling + L2 normalize) — and avoids pulling
# torch/optimum/onnxscript into the build.
FROM alpine:3.20 AS model

ARG MODEL_ONNX_URL=https://huggingface.co/Xenova/all-MiniLM-L6-v2/resolve/main/onnx/model.onnx
ARG VOCAB_URL=https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/resolve/main/vocab.txt
RUN apk add --no-cache curl ca-certificates && \
    mkdir -p /models && \
    curl -fsSL -o /models/model.onnx "${MODEL_ONNX_URL}" && \
    curl -fsSL -o /models/vocab.txt "${VOCAB_URL}"

# ---- Runtime stage ----
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    git git-lfs ca-certificates curl && \
    rm -rf /var/lib/apt/lists/* && \
    git lfs install

# Install ONNX Runtime shared library.
# Note: use a version that publishes the CPU linux-x64 tarball
# (onnxruntime-linux-x64-<ver>.tgz). v1.21.1 only shipped GPU assets.
ARG ONNX_VERSION=1.21.0
RUN curl -fsSL https://github.com/microsoft/onnxruntime/releases/download/v${ONNX_VERSION}/onnxruntime-linux-x64-${ONNX_VERSION}.tgz \
    | tar xz -C /tmp && \
    cp /tmp/onnxruntime-linux-x64-${ONNX_VERSION}/lib/libonnxruntime.so.${ONNX_VERSION} /usr/lib/ && \
    ln -s /usr/lib/libonnxruntime.so.${ONNX_VERSION} /usr/lib/libonnxruntime.so && \
    rm -rf /tmp/onnxruntime-*

COPY --from=build /bin/admont-ai-api /usr/local/bin/admont-ai-api
COPY --from=model /models/model.onnx /models/model.onnx
COPY --from=model /models/vocab.txt /models/vocab.txt

ENV SEARCH_ONNX_RUNTIME_PATH=/usr/lib/libonnxruntime.so
ENV SEARCH_MODEL_PATH=/models/model.onnx
ENV SEARCH_VOCAB_PATH=/models/vocab.txt

EXPOSE 8080
ENTRYPOINT ["admont-ai-api"]
