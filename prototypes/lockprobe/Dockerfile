# THROWAWAY — wayfinder ticket #96.
#
# The real app ships FROM scratch. This image uses alpine instead, on purpose:
# the probe has to be driven interactively (baseline, then -arm, then verify),
# and `fly ssh console` needs a shell to land in. The base image is irrelevant
# to the question being asked — the volume is the volume either way.
#
# Do not grow this into anything.

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /lockprobe ./prototypes/lockprobe/

FROM alpine:3
COPY --from=build /lockprobe /lockprobe
# -hold keeps PID 1 alive after the report, so the platform doesn't treat the
# container as crashed and restart-loop it before you can read the output.
ENTRYPOINT ["/lockprobe", "-dir", "/data", "-hold"]
