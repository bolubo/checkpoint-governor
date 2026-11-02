# The image is the shortest path: one command, no Go on the machine, no cluster.
#
#   docker build -t governor .
#   docker run -v /ckpt:/ckpt governor run --dir /ckpt
#
# It is built from a pinned toolchain and shipped on scratch: the binary is static, it needs no
# shell, no CA bundle and no /tmp, and the only thing it touches is the directory you mount.
# There are no third-party dependencies (standard library only), so there is no module download.
FROM golang:1.26 AS build
ARG VERSION=dev
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/ckptctl ./cmd/ckptctl \
	&& CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/ckpttrainer ./cmd/ckpttrainer \
	&& CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/contrast ./experiments/contrast/main.go

FROM scratch
COPY --from=build /out/ckptctl /ckptctl
# The contrast table runs in the image too: `docker run --rm --tmpfs /tmp --entrypoint /contrast <image>`.
# It writes scratch directories under TMPDIR, and a scratch image has no /tmp, so the image carries
# one. Run it with `--tmpfs /tmp`: on overlay2 the loop's per-round publish costs ~12 ms of fsync.
# The controller itself still needs nothing but the directory you mount.
WORKDIR /tmp
COPY --from=build /out/contrast /contrast
# The demo job is in the image too, so a second container can write checkpoints for the first one to
# govern without pulling anything else.
COPY --from=build /out/ckpttrainer /ckpttrainer
ENTRYPOINT ["/ckptctl"]
