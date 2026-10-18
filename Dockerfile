FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -o /out/fetchmodel ./cmd/fetchmodel
ENV GO_POTION_HOME=/models
RUN mkdir -p /models && /out/fetchmodel

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server
COPY --from=build --chown=nonroot:nonroot /models /models
ENV GO_POTION_HOME=/models GIN_MODE=release
EXPOSE 8080
ENTRYPOINT ["/server"]
