FROM golang:1.21 AS builder
WORKDIR /app
# РФ-зеркало: deb.debian.org из России почти не качается
RUN sed -i 's|http://deb.debian.org/debian|http://mirror.yandex.ru/debian|g' /etc/apt/sources.list.d/debian.sources
COPY go.mod go.sum *.go ./
RUN go mod download && go build -o vlesssubtest .

# Download Xray-core binary (used for xhttp transport tests)
RUN apt-get update && apt-get install -y --no-install-recommends unzip wget ca-certificates && \
    wget -q "https://github.com/XTLS/Xray-core/releases/download/v26.3.27/Xray-linux-64.zip" -O /tmp/xray.zip && \
    unzip -o /tmp/xray.zip -d /tmp/xray && \
    mv /tmp/xray/xray /tmp/xray-bin && \
    chmod +x /tmp/xray-bin && \
    rm -rf /tmp/xray.zip /tmp/xray

FROM debian:bookworm-slim AS runtime
# РФ-зеркало: deb.debian.org из России почти не качается
RUN sed -i 's|http://deb.debian.org/debian|http://mirror.yandex.ru/debian|g' /etc/apt/sources.list.d/debian.sources
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates wget && \
    rm -rf /var/lib/apt/lists/*
ARG SING_BOX_VERSION=1.13.13
RUN wget -q "https://github.com/SagerNet/sing-box/releases/download/v${SING_BOX_VERSION}/sing-box-${SING_BOX_VERSION}-linux-amd64.tar.gz" -O /tmp/sb.tar.gz && \
    tar -xzf /tmp/sb.tar.gz -C /tmp && \
    mv /tmp/sing-box-*/sing-box /usr/local/bin/ && \
    rm -rf /tmp/sb.tar.gz /tmp/sing-box-* && \
    chmod +x /usr/local/bin/sing-box
COPY --from=builder /tmp/xray-bin /usr/local/bin/xray
COPY --from=builder /app/vlesssubtest /usr/local/bin/
EXPOSE 8080
ENTRYPOINT ["vlesssubtest"]
