# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS builder

WORKDIR /src
COPY go.mod ./
COPY . .

ARG VERSION=0.1.0
ARG COMMIT=
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/qeinstein/paradox/version.Version=${VERSION} -X github.com/qeinstein/paradox/version.Commit=${COMMIT}" \
    -o /out/paradox ./cmd/paradox

FROM alpine:3.22

RUN apk add --no-cache ca-certificates python3 util-linux \
    && addgroup -S paradox \
    && adduser -S -G paradox -h /app paradox

WORKDIR /app
COPY --from=builder /out/paradox /usr/local/bin/paradox

RUN mkdir -p /data/runs /data/evals \
    && chown -R paradox:paradox /data

USER paradox
ENV PARADOX_RUNS_DIR=/data/runs \
    PARADOX_EVAL_DIR=/data/evals

VOLUME ["/data"]
EXPOSE 8080

ENTRYPOINT ["paradox"]
CMD ["serve", "--addr", "0.0.0.0:8080"]
