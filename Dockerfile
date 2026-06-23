# build
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY main.go ./
COPY web ./web
RUN go mod tidy && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /lootlist .

# run
FROM gcr.io/distroless/static-debian12
COPY --from=build /lootlist /lootlist
ENV LOOTLIST_DB=/data/lootlist.db
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/lootlist"]
