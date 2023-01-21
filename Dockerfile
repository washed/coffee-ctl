FROM --platform=$BUILDPLATFORM golang:alpine AS build
ARG TARGETPLATFORM
ARG BUILDPLATFORM

COPY . /usr/src/app/

WORKDIR /usr/src/app/

RUN GOOS=$(echo $TARGETPLATFORM | cut -d "/" -f 1) GOARCH=$(echo $TARGETPLATFORM | cut -d "/" -f 2) CGO_ENABLED=0 go build -o build/coffee-ctl ./cmd/coffee-ctl

FROM alpine
RUN apk --no-cache add ca-certificates
COPY --from=build /usr/src/app/build/coffee-ctl /coffee-ctl
ENTRYPOINT ["/coffee-ctl"]
