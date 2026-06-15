# ---- Build stage ----
FROM golang:1.25-bookworm AS build

RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates gcc libc6-dev && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -o /bin/md-wiki-server .

# ---- Model stage ----
FROM python:3.12-slim-bookworm AS model

RUN pip install --no-cache-dir optimum[exporters] onnx onnxruntime transformers torch --extra-index-url https://download.pytorch.org/whl/cpu
RUN optimum-cli export onnx --model sentence-transformers/all-MiniLM-L6-v2 /models && \
    cp /models/tokenizer/vocab.txt /models/vocab.txt

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

COPY --from=build /bin/md-wiki-server /usr/local/bin/md-wiki-server
COPY --from=model /models/model.onnx /models/model.onnx
COPY --from=model /models/vocab.txt /models/vocab.txt

ENV SEARCH_ONNX_RUNTIME_PATH=/usr/lib/libonnxruntime.so
ENV SEARCH_MODEL_PATH=/models/model.onnx
ENV SEARCH_VOCAB_PATH=/models/vocab.txt

EXPOSE 8080
ENTRYPOINT ["md-wiki-server"]
