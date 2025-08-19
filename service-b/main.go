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
	"go.opentelemetry.io/otel/exporters/zipkin"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
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

func initTracer() {
	exporter, err := zipkin.New("http://zipkin:9411/api/v2/spans")
	if err != nil {
		log.Fatal(err)
	}

	batcher := sdktrace.NewBatchSpanProcessor(exporter)
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(batcher),
	)
	otel.SetTracerProvider(tp)
	tracer = tp.Tracer("service-b")
}

func validateCEP(cep string) bool {
	matched, _ := regexp.MatchString(`^\d{8}$`, cep)
	return matched
}

func getLocationFromCEP(ctx context.Context, cep string) (*ViaCEPResponse, error) {
	_, span := tracer.Start(ctx, "get_location_from_cep")
	defer span.End()

	url := fmt.Sprintf("https://viacep.com.br/ws/%s/json/", cep)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var viaCEPResp ViaCEPResponse
	if err := json.Unmarshal(body, &viaCEPResp); err != nil {
		return nil, err
	}

	return &viaCEPResp, nil
}

func getWeather(ctx context.Context, city string) (*WeatherResponse, error) {
	_, span := tracer.Start(ctx, "get_weather")
	defer span.End()

	// Obter API key do ambiente
	apiKey := os.Getenv("WEATHER_API_KEY")
	if apiKey == "" {
		// Fallback para dados mock se não houver API key
		log.Printf("WeatherAPI: API key não configurada, usando dados simulados para %s", city)
		weatherResp := &WeatherResponse{
			Current: struct {
				TempC float64 `json:"temp_c"`
			}{
				TempC: 25.0, // Temperatura mock
			},
		}
		return weatherResp, nil
	}

	// Construir URL da API
	url := fmt.Sprintf("https://api.weatherapi.com/v1/current.json?key=%s&q=%s&aqi=no", apiKey, city)

	// Fazer requisição HTTP
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error making request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("weather API error: %s - %s", resp.Status, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response: %w", err)
	}

	var weatherResp WeatherResponse
	if err := json.Unmarshal(body, &weatherResp); err != nil {
		return nil, fmt.Errorf("error parsing response: %w", err)
	}

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

	ctx, span := tracer.Start(r.Context(), "handle_weather_request")
	defer span.End()

	var req CEPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Validação do CEP
	if !validateCEP(req.CEP) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte("invalid zipcode"))
		return
	}

	// Busca localização pelo CEP
	log.Printf("Buscando localização para CEP: %s", req.CEP)
	location, err := getLocationFromCEP(ctx, req.CEP)
	if err != nil {
		log.Printf("Erro ao buscar localização: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if location.Erro {
		log.Printf("CEP não encontrado: %s", req.CEP)
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("can not find zipcode"))
		return
	}

	// Busca temperatura
	log.Printf("Buscando temperatura para: %s", location.Localidade)
	weather, err := getWeather(ctx, location.Localidade)
	if err != nil {
		log.Printf("Erro ao buscar temperatura: %v", err)
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

	log.Printf("Sucesso! Cidade: %s, Temperatura: %.1f°C (%.1f°F, %.1fK)",
		location.Localidade, weather.Current.TempC, tempF, tempK)

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
