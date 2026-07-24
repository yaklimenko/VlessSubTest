FROM golang:1.21 AS builder
WORKDIR /app
COPY go.mod *.go ./
RUN go build -o vlesssubtest .

FROM debian:bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates wget && \
    rm -rf /var/lib/apt/lists/*
ARG SING_BOX_VERSION=1.13.13
RUN wget -q "https://github.com/SagerNet/sing-box/releases/download/v${SING_BOX_VERSION}/sing-box-${SING_BOX_VERSION}-linux-amd64.tar.gz" -O /tmp/sb.tar.gz && \
    tar -xzf /tmp/sb.tar.gz -C /tmp && \
    mv /tmp/sing-box-*/sing-box /usr/local/bin/ && \
    rm -rf /tmp/sb.tar.gz /tmp/sing-box-* && \
    chmod +x /usr/local/bin/sing-box
COPY --from=builder /app/vlesssubtest /usr/local/bin/
EXPOSE 8080
ENTRYPOINT ["vlesssubtest"]
