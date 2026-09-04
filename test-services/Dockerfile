#eval $(minikube docker-env)

FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY main.go .
RUN CGO_ENABLED=0 go build -o fake-api .

FROM scratch
COPY --from=builder /app/fake-api /fake-api
EXPOSE 8080
ENTRYPOINT ["/fake-api"]
