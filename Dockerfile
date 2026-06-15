# ---- Build stage ----
FROM golang:1.25-bookworm AS build

RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates gcc libc6-dev && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -o /bin/admont-ai-api .

# ---- Model stage ----
FROM python:3.12-slim-bookworm AS model

# Pin optimum to the last 1.x: optimum 2.x moved the ONNX exporter into a
# separate `optimum-onnx` package, so `optimum-cli export onnx` is no longer
# registered by `optimum[exporters]` alone.
RUN pip install --no-cache-dir "optimum[exporters]==1.24.0" onnx onnxruntime transformers torch --extra-index-url https://download.pytorch.org/whl/cpu
RUN optimum-cli export onnx --model sentence-transformers/all-MiniLM-L6-v2 /models && \
    if [ ! -f /models/vocab.txt ]; then cp "$(find /models -name vocab.txt | head -1)" /models/vocab.txt; fi

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
