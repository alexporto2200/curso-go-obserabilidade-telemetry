package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/zipkin"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"
)

type CEPRequest struct {
	CEP string `json:"cep"`
}

type CEPResponse struct {
	City  string  `json:"city"`
	TempC float64 `json:"temp_C"`
	TempF float64 `json:"temp_F"`
	TempK float64 `json:"temp_K"`
}

var tracer trace.Tracer
var propagator propagation.TextMapPropagator

func initTracer() {
	exporter, err := zipkin.New("http://zipkin:9411/api/v2/spans")
	if err != nil {
		log.Fatal(err)
	}

	batcher := sdktrace.NewBatchSpanProcessor(exporter)
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(batcher),
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName("cep-service"),
		)),
	)
	otel.SetTracerProvider(tp)
	propagator = otel.GetTextMapPropagator()
	tracer = tp.Tracer("cep-service")
}

func validateCEP(cep string) bool {
	// Verifica se tem exatamente 8 dígitos
	matched, _ := regexp.MatchString(`^\d{8}$`, cep)
	return matched
}

func handleCEP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, span := tracer.Start(r.Context(), "handle_cep_request")
	defer span.End()

	// Adicionar atributos detalhados ao span
	span.SetAttributes(
		semconv.HTTPMethod(r.Method),
		semconv.HTTPRoute("/cep"),
		attribute.String("http.request.header.content-type", r.Header.Get("content-type")),
		attribute.String("http.request.header.user-agent", r.Header.Get("user-agent")),
		semconv.NetHostName("service-a"),
		semconv.NetHostPort(8080),
	)

	var req CEPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		span.SetAttributes(semconv.HTTPStatusCode(400))
		span.SetStatus(codes.Error, "Invalid JSON")
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Adicionar CEP como atributo do span
	span.SetAttributes(semconv.HTTPRequestBodySize(len(req.CEP)))
	span.AddEvent("CEP received", trace.WithAttributes(
		semconv.HTTPRequestBodySize(len(req.CEP)),
	))

	// Validação do CEP
	if !validateCEP(req.CEP) {
		log.Printf("CEP inválido: %s", req.CEP)
		span.SetAttributes(
			semconv.HTTPStatusCode(422),
			semconv.HTTPResponseBodySize(len("invalid zipcode")),
		)
		span.SetStatus(codes.Error, "CEP inválido")
		span.AddEvent("CEP validation failed", trace.WithAttributes(
			semconv.HTTPStatusCode(422),
		))
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte("invalid zipcode"))
		return
	}

	span.AddEvent("CEP validation successful")

	// Chama o Serviço B
	log.Printf("Encaminhando CEP %s para o Service B", req.CEP)
	serviceBURL := "http://service-b:8081/weather"
	requestBody, _ := json.Marshal(req)

	ctx, callSpan := tracer.Start(ctx, "call_weather_service")
	defer callSpan.End()

	// Adicionar atributos detalhados ao span da chamada
	callSpan.SetAttributes(
		semconv.HTTPMethod("POST"),
		semconv.HTTPURL(serviceBURL),
		attribute.String("http.request.header.content-type", "application/json"),
		semconv.HTTPRequestBodySize(len(requestBody)),
		semconv.PeerService("weather-service"),
		attribute.String("peer.host", "service-b"),
		attribute.Int("peer.port", 8081),
	)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", serviceBURL, bytes.NewBuffer(requestBody))
	if err != nil {
		callSpan.SetStatus(codes.Error, "Failed to create request")
		log.Printf("Erro ao criar requisição: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// Propagar o contexto de trace para o Service B
	propagator.Inject(ctx, propagation.HeaderCarrier(httpReq.Header))

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		callSpan.SetStatus(codes.Error, "Failed to call weather service")
		log.Printf("Erro ao chamar Service B: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	// Adicionar informações da resposta ao span
	callSpan.SetAttributes(
		semconv.HTTPStatusCode(resp.StatusCode),
		attribute.String("http.response.header.content-type", resp.Header.Get("content-type")),
	)

	// Repassa a resposta do Serviço B
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)

	if resp.StatusCode == http.StatusOK {
		var weatherResp CEPResponse
		if err := json.NewDecoder(resp.Body).Decode(&weatherResp); err != nil {
			span.SetStatus(codes.Error, "Failed to decode response")
			http.Error(w, "Error decoding response", http.StatusInternalServerError)
			return
		}

		// Adicionar informações da resposta ao span principal
		span.SetAttributes(
			semconv.HTTPStatusCode(200),
			semconv.HTTPResponseBodySize(len(fmt.Sprintf("%+v", weatherResp))),
		)

		span.AddEvent("Weather data retrieved", trace.WithAttributes(
			semconv.HTTPStatusCode(200),
		))

		json.NewEncoder(w).Encode(weatherResp)
	} else {
		// Repassa mensagem de erro
		body := make([]byte, 1024)
		n, _ := resp.Body.Read(body)
		span.SetStatus(codes.Error, "Weather service error")
		w.Write(body[:n])
	}
}

func main() {
	initTracer()

	http.HandleFunc("/cep", handleCEP)

	log.Println("Service A iniciado na porta 8080")
	log.Println("Endpoint: /cep")
	log.Println("Zipkin: http://localhost:9411")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
