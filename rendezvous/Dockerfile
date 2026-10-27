# Rendezvous image: ushd, the one public port, the registry, the menu, and the
# tunnel every shared terminal rides. Build from the repo root.
#
# There is no sshd here, and nothing for a shell to do: ushd terminates SSH
# itself and runs each session as a child of its own binary on a real pty.
# The build stage runs on the builder's own platform and cross-compiles, so
# an arm64 image costs no emulated Go build.
FROM --platform=$BUILDPLATFORM golang:1-alpine AS build
ARG TARGETOS TARGETARCH
ENV CGO_ENABLED=0
# Before the source, so a code change does not reinstall it.
RUN go install github.com/google/go-licenses/v2@v2.0.1
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/ushd ./cmd/ushd
COPY internal ./internal
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /go/bin/ushd ./cmd/ushd

# The licenses of everything compiled into ushd, which their terms ask to ship
# with it: each module's, Go's own (go-licenses leaves out the standard
# library), and upterm's for internal/termio.
COPY LICENSE ./
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go-licenses save ./cmd/ushd --save_path=/licenses \
 && mkdir -p /licenses/go /licenses/ush/internal/termio \
 && cp "$(go env GOROOT)/LICENSE" /licenses/go/ \
 && cp internal/termio/LICENSE /licenses/ush/internal/termio/

FROM alpine:3.21
# tini reaps orphans that outlive a session's child; ushd waits for its own.
RUN apk add --no-cache tini \
 && adduser -D -H -u 1000 -s /sbin/nologin ush \
 && mkdir -p /run/ush \
 && chown ush:ush /run/ush
COPY --from=build /go/bin/ushd /usr/local/bin/
COPY --from=build /licenses /usr/share/licenses/ush/
USER ush
EXPOSE 7874
ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/ushd"]
