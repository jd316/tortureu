# TortureU as a CI job image (SPEC.md R-CLI-11, TBD-11).
#
# `tortureu init --ci gitlab` writes a job that runs *inside* this image: the
# binary arrives with the runner, so nothing is downloaded mid-job and the
# pin is the image tag.
#
# The base is docker:cli because tortureu's whole job is to bring a
# docker-compose stack up, inject faults into it and tear it down — an image
# with tortureu and no Docker client could not do any of that. It talks to a
# daemon, it does not contain one (GitLab: a docker:dind service; GitHub: the
# runner's own daemon).
#
# What is deliberately NOT here: k6. k6 is AGPL-3 and TortureU is MIT
# (SPEC.md §10). R-LIC-1 keeps them separate processes; not shipping k6 in
# this image keeps them separate distributions too, so R-LIC-3's obligations
# on a redistributed k6 never arise. Install k6 in the job, or derive an
# image from this one that adds it under its own licence.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Static, for the same reason .goreleaser.yaml builds static: the runtime
# stage is musl.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tortureu ./cmd/tortureu

FROM docker:cli
COPY --from=build /out/tortureu /usr/local/bin/tortureu
COPY --from=build /src/LICENSE /usr/share/licenses/tortureu/LICENSE
# No ENTRYPOINT: this is a CI *job* image, and a GitLab job's script runs as
# the container's command. An entrypoint of `tortureu` would swallow every
# other command the job needs to run (docker, sh, the install of k6).
CMD ["tortureu"]
