FROM ekidd/rust-musl-builder as rustBuilder
WORKDIR /home/rust/src
RUN git clone https://github.com/cloudflare/boringtun.git
WORKDIR boringtun
RUN rustup target add x86_64-unknown-linux-musl
RUN cargo build --bin boringtun --release

FROM golang:1.16 AS goBuilder
ENV PATH /go/bin:/usr/local/go/bin:$PATH
ENV GOPATH /go
WORKDIR /go/src/github.com/liqotech/liqo
COPY go.mod ./go.mod
COPY go.sum ./go.sum
RUN  go mod download
COPY . ./
WORKDIR /go/src/github.com/liqotech/liqo
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$(go env GOARCH) go build ./cmd/liqonet/
RUN cp liqonet /usr/bin/liqonet

FROM alpine:3.13.2
RUN apk update && apk add iptables bash wireguard-tools tcpdump
COPY --from=goBuilder /usr/bin/liqonet /usr/bin/liqonet
COPY --from=rustBuilder /home/rust/src/boringtun/target/x86_64-unknown-linux-musl/release/boringtun /usr/bin/boringtun
ENTRYPOINT [ "/usr/bin/liqonet" ]