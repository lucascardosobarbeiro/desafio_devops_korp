package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// newTestHandler cria um registry isolado para que os testes não compartilhem métricas globais.
func newTestHandler(t *testing.T, clock func() time.Time) http.Handler {
	t.Helper()
	registry := prometheus.NewRegistry()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app := newApplication(registry, clock, logger)
	return app.routes(registry)
}

// TestProjetoKorpHandler valida status, Content-Type e o contrato JSON exigido.
func TestProjetoKorpHandler(t *testing.T) {
	// O relógio usa UTC-3 para confirmar que o handler realmente converte a saída para UTC.
	fixedTime := time.Date(2026, time.September, 9, 13, 30, 0, 123, time.FixedZone("BRT", -3*60*60))
	handler := newTestHandler(t, func() time.Time { return fixedTime })
	request := httptest.NewRequest(http.MethodGet, "/projeto-korp", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status inesperado: obtido %d, esperado %d", response.Code, http.StatusOK)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type inesperado: %q", contentType)
	}

	var payload projetoKorpResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("JSON inválido: %v", err)
	}
	if payload.Nome != "Projeto Korp" {
		t.Errorf("nome inesperado: %q", payload.Nome)
	}
	if payload.Horario != fixedTime.UTC().Format(time.RFC3339Nano) {
		t.Errorf("horário inesperado: %q", payload.Horario)
	}
}

// TestHorarioAtualizadoPorRequisicao comprova que o horário não é calculado só no startup.
func TestHorarioAtualizadoPorRequisicao(t *testing.T) {
	times := []time.Time{
		time.Date(2026, time.September, 9, 16, 30, 0, 0, time.UTC),
		time.Date(2026, time.September, 9, 16, 30, 1, 0, time.UTC),
	}
	call := 0
	handler := newTestHandler(t, func() time.Time {
		current := times[call]
		call++
		return current
	})

	for index, expected := range times {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/projeto-korp", nil))

		var payload projetoKorpResponse
		if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
			t.Fatalf("requisição %d retornou JSON inválido: %v", index+1, err)
		}
		if payload.Horario != expected.Format(time.RFC3339Nano) {
			t.Errorf("requisição %d: obtido %q, esperado %q", index+1, payload.Horario, expected.Format(time.RFC3339Nano))
		}
	}
}

// TestMetodoNaoPermitido garante que o endpoint aceite somente GET.
func TestMetodoNaoPermitido(t *testing.T) {
	handler := newTestHandler(t, time.Now)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/projeto-korp", nil))

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status inesperado: obtido %d, esperado %d", response.Code, http.StatusMethodNotAllowed)
	}
	if allow := response.Header().Get("Allow"); allow != http.MethodGet {
		t.Errorf("header Allow inesperado: %q", allow)
	}
}

// TestHealthEMetricas verifica os endpoints operacionais usados por Docker e Prometheus.
func TestHealthEMetricas(t *testing.T) {
	handler := newTestHandler(t, time.Now)

	projectResponse := httptest.NewRecorder()
	handler.ServeHTTP(projectResponse, httptest.NewRequest(http.MethodGet, "/projeto-korp", nil))

	healthResponse := httptest.NewRecorder()
	handler.ServeHTTP(healthResponse, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if healthResponse.Code != http.StatusOK || !strings.Contains(healthResponse.Body.String(), `"status":"ok"`) {
		t.Fatalf("healthcheck inválido: status=%d body=%q", healthResponse.Code, healthResponse.Body.String())
	}

	metricsResponse := httptest.NewRecorder()
	handler.ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if metricsResponse.Code != http.StatusOK {
		t.Fatalf("endpoint de métricas retornou status %d", metricsResponse.Code)
	}
	expectedMetric := `projeto_korp_http_requests_total{method="GET",route="/projeto-korp",status="200"} 1`
	if !strings.Contains(metricsResponse.Body.String(), expectedMetric) {
		t.Fatalf("métrica esperada não encontrada: %s", expectedMetric)
	}
}
