FROM golang:1.27.1-bookworm AS dependencies

WORKDIR /app

ENV GOTOOLCHAIN=local

COPY go.mod go.sum ./

RUN go mod download && go mod verify

FROM dependencies AS source

COPY . .

FROM source AS test

ENV CGO_ENABLED=1

CMD ["go", "test", "-race", "-count=1", "./..."]

FROM source AS build

RUN CGO_ENABLED=0 go build \
    -trimpath \
    -buildvcs=false \
    -o /out/service \
    ./cmd/service

FROM scratch AS runtime

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/service /service

USER 65532:65532

EXPOSE 8080

ENTRYPOINT ["/service"]