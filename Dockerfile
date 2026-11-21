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

# ---- ONNX Runtime stage ----
# Must match the ORT C API version of github.com/yalue/onnxruntime_go in go.mod
# (v1.19.0 -> onnxruntime 1.21.x); a mismatch fails at startup with
# "Error setting ORT API base". Fetched from the official Microsoft NuGet
# package because the GitHub release downloads started returning 404.
FROM alpine:3.20 AS onnxruntime
ARG ONNX_VERSION=1.21.0
RUN apk add --no-cache curl ca-certificates unzip && \
    curl -fsSL -o /tmp/ort.nupkg "https://www.nuget.org/api/v2/package/Microsoft.ML.OnnxRuntime/${ONNX_VERSION}" && \
    mkdir -p /onnxruntime && \
    unzip -j /tmp/ort.nupkg runtimes/linux-x64/native/libonnxruntime.so -d /onnxruntime && \
    rm /tmp/ort.nupkg

# ---- Runtime stage ----
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    git git-lfs ca-certificates curl && \
    rm -rf /var/lib/apt/lists/* && \
    git lfs install

COPY --from=onnxruntime /onnxruntime/libonnxruntime.so /usr/lib/libonnxruntime.so
COPY --from=build /bin/admont-ai-api /usr/local/bin/admont-ai-api
COPY --from=model /models/model.onnx /models/model.onnx
COPY --from=model /models/vocab.txt /models/vocab.txt

ENV SEARCH_ONNX_RUNTIME_PATH=/usr/lib/libonnxruntime.so
ENV SEARCH_MODEL_PATH=/models/model.onnx
ENV SEARCH_VOCAB_PATH=/models/vocab.txt

EXPOSE 8080
ENTRYPOINT ["admont-ai-api"]
