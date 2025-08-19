package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
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

type ViaCEPResponse struct {
	Localidade string `json:"localidade"`
	UF         string `json:"uf"`
	Erro       bool   `json:"erro"`
}

type WeatherResponse struct {
	Current struct {
		TempC float64 `json:"temp_c"`
	} `json:"current"`
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
			semconv.ServiceName("weather-service"),
		)),
	)
	otel.SetTracerProvider(tp)
	propagator = otel.GetTextMapPropagator()
	tracer = tp.Tracer("weather-service")
}

func validateCEP(cep string) bool {
	matched, _ := regexp.MatchString(`^\d{8}$`, cep)
	return matched
}

func getLocationFromCEP(ctx context.Context, cep string) (*ViaCEPResponse, error) {
	_, span := tracer.Start(ctx, "get_location_from_cep")
	defer span.End()

	span.SetAttributes(
		attribute.String("cep", cep),
		attribute.String("api.endpoint", "viacep.com.br"),
	)

	url := fmt.Sprintf("https://viacep.com.br/ws/%s/json/", cep)

	span.AddEvent("Making HTTP request to ViaCEP", trace.WithAttributes(
		attribute.String("http.url", url),
	))

	resp, err := http.Get(url)
	if err != nil {
		span.SetStatus(codes.Error, "Failed to call ViaCEP API")
		span.RecordError(err)
		return nil, err
	}
	defer resp.Body.Close()

	span.SetAttributes(
		semconv.HTTPStatusCode(resp.StatusCode),
		attribute.String("http.response.header.content-type", resp.Header.Get("content-type")),
	)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		span.SetStatus(codes.Error, "Failed to read response body")
		span.RecordError(err)
		return nil, err
	}

	span.SetAttributes(semconv.HTTPResponseBodySize(len(body)))

	var viaCEPResp ViaCEPResponse
	if err := json.Unmarshal(body, &viaCEPResp); err != nil {
		span.SetStatus(codes.Error, "Failed to parse ViaCEP response")
		span.RecordError(err)
		return nil, err
	}

	if viaCEPResp.Erro {
		span.SetStatus(codes.Error, "CEP not found")
		span.AddEvent("CEP not found in ViaCEP", trace.WithAttributes(
			attribute.String("cep", cep),
		))
	} else {
		span.AddEvent("Location found", trace.WithAttributes(
			attribute.String("city", viaCEPResp.Localidade),
			attribute.String("state", viaCEPResp.UF),
		))
	}

	return &viaCEPResp, nil
}

func getWeather(ctx context.Context, city string) (*WeatherResponse, error) {
	_, span := tracer.Start(ctx, "get_weather")
	defer span.End()

	span.SetAttributes(
		attribute.String("city", city),
	)

	// Obter API key do ambiente
	apiKey := os.Getenv("WEATHER_API_KEY")
	if apiKey == "" {
		// Fallback para dados mock se não houver API key
		log.Printf("WeatherAPI: API key não configurada, usando dados simulados para %s", city)
		span.AddEvent("Using mock weather data", trace.WithAttributes(
			attribute.String("reason", "no_api_key"),
		))

		weatherResp := &WeatherResponse{
			Current: struct {
				TempC float64 `json:"temp_c"`
			}{
				TempC: 25.0, // Temperatura mock
			},
		}

		span.AddEvent("Mock weather data generated", trace.WithAttributes(
			attribute.Float64("temperature.celsius", weatherResp.Current.TempC),
		))

		return weatherResp, nil
	}

	// Construir URL da API
	url := fmt.Sprintf("https://api.weatherapi.com/v1/current.json?key=%s&q=%s&aqi=no", apiKey, city)

	span.AddEvent("Making HTTP request to WeatherAPI", trace.WithAttributes(
		attribute.String("http.url", url),
	))

	// Fazer requisição HTTP
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		span.SetStatus(codes.Error, "Failed to create weather request")
		span.RecordError(err)
		return nil, fmt.Errorf("error creating request: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		span.SetStatus(codes.Error, "Failed to call WeatherAPI")
		span.RecordError(err)
		return nil, fmt.Errorf("error making request: %w", err)
	}
	defer resp.Body.Close()

	span.SetAttributes(
		semconv.HTTPStatusCode(resp.StatusCode),
		attribute.String("http.response.header.content-type", resp.Header.Get("content-type")),
	)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		span.SetStatus(codes.Error, "WeatherAPI returned error")
		span.RecordError(fmt.Errorf("weather API error: %s - %s", resp.Status, string(body)))
		return nil, fmt.Errorf("weather API error: %s - %s", resp.Status, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		span.SetStatus(codes.Error, "Failed to read weather response")
		span.RecordError(err)
		return nil, fmt.Errorf("error reading response: %w", err)
	}

	span.SetAttributes(semconv.HTTPResponseBodySize(len(body)))

	var weatherResp WeatherResponse
	if err := json.Unmarshal(body, &weatherResp); err != nil {
		span.SetStatus(codes.Error, "Failed to parse weather response")
		span.RecordError(err)
		return nil, fmt.Errorf("error parsing response: %w", err)
	}

	span.AddEvent("Weather data retrieved", trace.WithAttributes(
		attribute.Float64("temperature.celsius", weatherResp.Current.TempC),
	))

	return &weatherResp, nil
}

func convertTemperatures(tempC float64) (float64, float64) {
	tempF := tempC*1.8 + 32
	tempK := tempC + 273
	return tempF, tempK
}

func handleWeather(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extrair o contexto de trace da requisição
	ctx := propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	ctx, span := tracer.Start(ctx, "handle_weather_request")
	defer span.End()

	// Adicionar atributos detalhados ao span
	span.SetAttributes(
		semconv.HTTPMethod(r.Method),
		semconv.HTTPRoute("/weather"),
		attribute.String("http.request.header.content-type", r.Header.Get("content-type")),
		attribute.String("http.request.header.user-agent", r.Header.Get("user-agent")),
		semconv.NetHostName("service-b"),
		semconv.NetHostPort(8081),
	)

	var req CEPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		span.SetAttributes(semconv.HTTPStatusCode(400))
		span.SetStatus(codes.Error, "Invalid JSON")
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	span.SetAttributes(
		semconv.HTTPRequestBodySize(len(req.CEP)),
		attribute.String("cep", req.CEP),
	)

	span.AddEvent("CEP request received", trace.WithAttributes(
		attribute.String("cep", req.CEP),
	))

	// Validação do CEP
	if !validateCEP(req.CEP) {
		span.SetAttributes(
			semconv.HTTPStatusCode(422),
			semconv.HTTPResponseBodySize(len("invalid zipcode")),
		)
		span.SetStatus(codes.Error, "Invalid CEP format")
		span.AddEvent("CEP validation failed", trace.WithAttributes(
			attribute.String("cep", req.CEP),
		))
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte("invalid zipcode"))
		return
	}

	span.AddEvent("CEP validation successful")

	// Busca localização pelo CEP
	log.Printf("Buscando localização para CEP: %s", req.CEP)
	location, err := getLocationFromCEP(ctx, req.CEP)
	if err != nil {
		log.Printf("Erro ao buscar localização: %v", err)
		span.SetStatus(codes.Error, "Failed to get location")
		span.RecordError(err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if location.Erro {
		log.Printf("CEP não encontrado: %s", req.CEP)
		span.SetAttributes(
			semconv.HTTPStatusCode(404),
			semconv.HTTPResponseBodySize(len("can not find zipcode")),
		)
		span.SetStatus(codes.Error, "CEP not found")
		span.AddEvent("CEP not found in database", trace.WithAttributes(
			attribute.String("cep", req.CEP),
		))
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("can not find zipcode"))
		return
	}

	// Busca temperatura
	log.Printf("Buscando temperatura para: %s", location.Localidade)
	weather, err := getWeather(ctx, location.Localidade)
	if err != nil {
		log.Printf("Erro ao buscar temperatura: %v", err)
		span.SetStatus(codes.Error, "Failed to get weather")
		span.RecordError(err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Converte temperaturas
	tempF, tempK := convertTemperatures(weather.Current.TempC)

	response := CEPResponse{
		City:  location.Localidade,
		TempC: weather.Current.TempC,
		TempF: tempF,
		TempK: tempK,
	}

	responseBody, _ := json.Marshal(response)
	span.SetAttributes(
		semconv.HTTPStatusCode(200),
		semconv.HTTPResponseBodySize(len(responseBody)),
		attribute.String("city", location.Localidade),
		attribute.Float64("temperature.celsius", weather.Current.TempC),
		attribute.Float64("temperature.fahrenheit", tempF),
		attribute.Float64("temperature.kelvin", tempK),
	)

	log.Printf("Sucesso! Cidade: %s, Temperatura: %.1f°C (%.1f°F, %.1fK)",
		location.Localidade, weather.Current.TempC, tempF, tempK)

	span.AddEvent("Weather data processed successfully", trace.WithAttributes(
		attribute.String("city", location.Localidade),
		attribute.Float64("temperature.celsius", weather.Current.TempC),
	))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

func main() {
	initTracer()

	http.HandleFunc("/weather", handleWeather)

	log.Println("Service B iniciado na porta 8081")
	log.Println("Endpoint: /weather")
	log.Println("Zipkin: http://localhost:9411")
	log.Fatal(http.ListenAndServe(":8081", nil))
}
