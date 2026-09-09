package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	// A porta é fixa porque 8080 faz parte do contrato definido pelo desafio.
	serverAddress     = ":8080"
	gracefulTimeout   = 10 * time.Second
	healthcheckTarget = "http://127.0.0.1:8080/healthz"
)

// projetoKorpResponse representa exatamente o JSON solicitado no enunciado.
type projetoKorpResponse struct {
	Nome    string `json:"nome"`
	Horario string `json:"horario"`
}

// application reúne dependências do servidor para facilitar testes e evitar estado global.
type application struct {
	clock           func() time.Time
	logger          *slog.Logger
	requestCounter  *prometheus.CounterVec
	requestDuration *prometheus.HistogramVec
}

// statusRecorder captura o status enviado pelo handler para rotular as métricas corretamente.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (recorder *statusRecorder) WriteHeader(status int) {
	if recorder.status != 0 {
		return
	}
	recorder.status = status
	recorder.ResponseWriter.WriteHeader(status)
}

func (recorder *statusRecorder) Write(body []byte) (int, error) {
	if recorder.status == 0 {
		recorder.WriteHeader(http.StatusOK)
	}
	return recorder.ResponseWriter.Write(body)
}

// newApplication registra métricas com labels de cardinalidade controlada.
func newApplication(registerer prometheus.Registerer, clock func() time.Time, logger *slog.Logger) *application {
	app := &application{
		clock:  clock,
		logger: logger,
		requestCounter: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "projeto_korp",
				Subsystem: "http",
				Name:      "requests_total",
				Help:      "Total de requisicoes HTTP recebidas pelo servico.",
			},
			[]string{"method", "route", "status"},
		),
		requestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "projeto_korp",
				Subsystem: "http",
				Name:      "request_duration_seconds",
				Help:      "Duracao das requisicoes HTTP em segundos.",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"method", "route", "status"},
		),
	}

	registerer.MustRegister(app.requestCounter, app.requestDuration)

	// Inicializa a série principal para ela aparecer no dashboard antes da primeira chamada.
	app.requestCounter.WithLabelValues(http.MethodGet, "/projeto-korp", strconv.Itoa(http.StatusOK)).Add(0)

	return app
}

// routes centraliza o roteamento e mantém /metrics fora da própria contagem de negócio.
func (app *application) routes(gatherer prometheus.Gatherer) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/projeto-korp", app.instrument("/projeto-korp", http.HandlerFunc(app.projetoKorpHandler)))
	mux.HandleFunc("/healthz", app.healthHandler)
	mux.Handle("/metrics", promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}))
	return mux
}

// projetoKorpHandler calcula o horário dentro do handler, garantindo atualização a cada request.
func (app *application) projetoKorpHandler(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		responseWriter.Header().Set("Allow", http.MethodGet)
		http.Error(responseWriter, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}

	responseWriter.Header().Set("Content-Type", "application/json; charset=utf-8")
	responseWriter.WriteHeader(http.StatusOK)

	response := projetoKorpResponse{
		Nome:    "Projeto Korp",
		Horario: app.clock().UTC().Format(time.RFC3339Nano),
	}

	if err := json.NewEncoder(responseWriter).Encode(response); err != nil {
		app.logger.Error("falha ao serializar resposta", "error", err)
	}
}

// healthHandler oferece uma verificação leve de disponibilidade para Docker e operadores.
func (app *application) healthHandler(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		responseWriter.Header().Set("Allow", http.MethodGet)
		http.Error(responseWriter, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}

	responseWriter.Header().Set("Content-Type", "application/json; charset=utf-8")
	responseWriter.WriteHeader(http.StatusOK)
	_, _ = responseWriter.Write([]byte("{\"status\":\"ok\"}\n"))
}

// instrument mede volume e latência somente com labels conhecidas, evitando alta cardinalidade.
func (app *application) instrument(route string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		startedAt := time.Now()
		recorder := &statusRecorder{ResponseWriter: responseWriter}

		next.ServeHTTP(recorder, request)
		if recorder.status == 0 {
			recorder.status = http.StatusOK
		}

		status := strconv.Itoa(recorder.status)
		app.requestCounter.WithLabelValues(request.Method, route, status).Inc()
		app.requestDuration.WithLabelValues(request.Method, route, status).Observe(time.Since(startedAt).Seconds())
		app.logger.Info("requisicao processada",
			"method", request.Method,
			"route", route,
			"status", recorder.status,
			"duration_ms", time.Since(startedAt).Milliseconds(),
		)
	})
}

// runHealthcheck usa o próprio binário na instrução HEALTHCHECK, mantendo a imagem final mínima.
func runHealthcheck() error {
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(healthcheckTarget)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck retornou status %d", response.StatusCode)
	}
	return nil
}

// runServer configura métricas de runtime, servidor com timeouts e encerramento gracioso.
func runServer() error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		prometheus.NewGoCollector(),
		prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}),
	)

	app := newApplication(registry, time.Now, logger)
	server := &http.Server{
		Addr:              serverAddress,
		Handler:           app.routes(registry),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	shutdownContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	serverError := make(chan error, 1)
	go func() {
		logger.Info("servidor iniciado", "address", serverAddress)
		serverError <- server.ListenAndServe()
	}()

	select {
	case err := <-serverError:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-shutdownContext.Done():
		logger.Info("encerramento solicitado")
	}

	contextWithTimeout, cancel := context.WithTimeout(context.Background(), gracefulTimeout)
	defer cancel()
	return server.Shutdown(contextWithTimeout)
}

// main seleciona entre o servidor normal e o modo interno usado pelo Docker HEALTHCHECK.
func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		if err := runHealthcheck(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if err := runServer(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
