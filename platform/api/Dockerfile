# syntax=docker/dockerfile:1
# Build context is platform/ (go.work spans api, sdk/go and examples/hello):
#   docker build -f platform/api/Dockerfile -t portal-platform-api:dev platform
FROM golang:1.27.2-trixie@sha256:47667934874913a449b96faa7ef4777fa285d9ec29126ee80ed61c45b7cccf0e AS build
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
WORKDIR /src
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd api && go build -trimpath -ldflags "-s -w" -o /out/platform-api ./cmd/platform-api

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=build /out/platform-api /platform-api
USER nonroot:nonroot
ENTRYPOINT ["/platform-api"]
