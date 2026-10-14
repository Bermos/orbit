# Build the web UI, then embed it into a static Go binary.
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/orbit ./cmd/orbit && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/orbit-runner ./cmd/orbit-runner

# git is needed at runtime for hosting and browsing repositories.
FROM alpine:3.21
RUN apk add --no-cache git git-daemon ca-certificates && adduser -D -u 10001 orbit
USER orbit
WORKDIR /home/orbit
COPY --from=build /out/orbit /out/orbit-runner /usr/local/bin/
ENV ORBIT_DATA_DIR=/data
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["orbit"]
