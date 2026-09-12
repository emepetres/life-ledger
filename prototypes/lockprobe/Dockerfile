# THROWAWAY — wayfinder ticket #96.
#
# Mirrors the real app's image shape (static CGO-free binary on scratch) so the
# probe sees the same filesystem the app would. Do not grow this into anything.

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /lockprobe ./prototypes/lockprobe/

FROM scratch
COPY --from=build /lockprobe /lockprobe
# -hold keeps PID 1 alive after the report, so Fly/Railway don't treat the
# container as crashed and restart-loop it before you can read the output.
ENTRYPOINT ["/lockprobe", "-dir", "/data", "-hold"]
