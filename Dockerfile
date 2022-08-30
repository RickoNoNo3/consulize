FROM golang:1.18-alpine3.16 AS build
WORKDIR /consulize-building
COPY *.go /consulize-building
COPY go.* /consulize-building
RUN go get
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o consulize .

FROM alpine:3.16 AS consulize
COPY --from=build /consulize-building/consulize /bin
ENTRYPOINT ["/bin/consulize"]
