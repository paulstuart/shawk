FROM golang:1.23.2

ENV PKG github.com/yuuki/shawk
WORKDIR /go/src/$PKG
