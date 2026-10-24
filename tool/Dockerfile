ARG GO_VERSION=1.25.6

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ENV CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH}
RUN go build -trimpath -ldflags="-s -w" -o /out/faketg ./cmd/faketg && \
    go build -trimpath -ldflags="-s -w" -o /out/proxy ./cmd/proxy && \
    go build -trimpath -ldflags="-s -w" -o /out/cgstats ./cmd/cgstats

FROM gcr.io/distroless/static-debian12:nonroot AS faketg
COPY --from=build /out/faketg /usr/local/bin/faketg
USER nonroot:nonroot
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/faketg"]

FROM gcr.io/distroless/static-debian12:nonroot AS proxy
COPY --from=build /out/proxy /usr/local/bin/proxy
USER nonroot:nonroot
EXPOSE 8019 9187
ENTRYPOINT ["/usr/local/bin/proxy"]

FROM gcr.io/distroless/static-debian12 AS cgstats
COPY --from=build /out/cgstats /usr/local/bin/cgstats
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/cgstats"]
