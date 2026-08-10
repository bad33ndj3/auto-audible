FROM golang:1.23-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /out/auto-audible .

FROM python:3.12-slim

RUN apt-get update \
	&& apt-get install -y --no-install-recommends ffmpeg ca-certificates \
	&& ffmpeg -version >/dev/null \
	&& rm -rf /var/lib/apt/lists/*

RUN pip install --no-cache-dir audible-cli \
	&& audible --version >/dev/null

WORKDIR /work

COPY --from=builder /out/auto-audible /usr/local/bin/auto-audible

ENTRYPOINT ["auto-audible"]
