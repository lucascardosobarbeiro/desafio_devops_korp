# syntax=docker/dockerfile:1

# Etapa de build: baixa dependências separadamente para aproveitar o cache de camadas.
FROM golang:1.27.1-alpine3.24 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

# Compila um binário estático, sem caminhos locais e sem símbolos de debug desnecessários.
COPY . .
RUN go test ./...
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/http-server-projeto-korp .

# Etapa final: scratch reduz superfície de ataque; o UID numérico impede execução como root.
FROM scratch
COPY --from=builder /out/http-server-projeto-korp /http-server-projeto-korp
USER 65532:65532
EXPOSE 8080

# O próprio binário testa /healthz, portanto a imagem mínima não precisa de curl ou shell.
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 CMD ["/http-server-projeto-korp", "healthcheck"]
ENTRYPOINT ["/http-server-projeto-korp"]
