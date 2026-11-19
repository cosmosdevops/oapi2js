# Drop-in replacement for an image built from instrumenta/openapi2jsonschema:
# same entrypoint arguments, same /out working directory.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /oapi2js .

FROM scratch
COPY --from=build /oapi2js /oapi2js
WORKDIR /out
ENTRYPOINT ["/oapi2js"]
